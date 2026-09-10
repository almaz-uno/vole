// Package daemon is vole's resident server: it keeps the whisper model in VRAM,
// listens on a platform IPC endpoint (unix socket or Windows named pipe), and
// records and transcribes audio on command.
package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/almaz-uno/vole/internal/audio"
	"github.com/almaz-uno/vole/internal/config"
	"github.com/almaz-uno/vole/internal/history"
	"github.com/almaz-uno/vole/internal/ipc"
	"github.com/almaz-uno/vole/internal/models"
	"github.com/almaz-uno/vole/internal/platform"
	"github.com/almaz-uno/vole/internal/postprocess"
	"github.com/almaz-uno/vole/internal/tray"
	"github.com/almaz-uno/vole/internal/whisper"
)

const threads = 8 // CPU threads for the non-GPU parts (mel, sampling)

// Daemon holds the dictation service state.
type Daemon struct {
	cfg     config.Config
	version string // build version, shown in the tray (menu title + tooltip)

	mu        sync.Mutex
	rec       *audio.Recorder
	recording bool
	byClick   bool // current recording was started by a tray click (→ clipboard only)
	mergeNext bool // current recording began while an earlier dictation was still in flight → it continues that text
	enabled   bool
	lang      string

	postOn     atomic.Bool // post-processing runtime toggle (tray checkbox); resets to cfg.PostProcessOn on restart
	postScript string      // configured post-processing script path; empty = feature unavailable

	englishInput atomic.Bool // English-input runtime toggle (tray checkbox); overrides base/shift language and disables translate-mode while on; resets to cfg.EnglishInput on restart

	ctx        *whisper.Context
	modelReady chan struct{} // closed once the model is loaded

	ind       platform.Indicator // overlay (X11/Windows) or Nop (Wayland tray-only); never nil
	inj       platform.Injector  // text injection backend; never nil
	hk        platform.Hotkey    // PTT source; nil if unavailable
	tr        *tray.Tray
	hist      *history.Store // recent dictations (tray menu + persisted file)
	levelStop chan struct{}  // stops the goroutine feeding the level to the indicator

	merge atomic.Bool // merge runtime toggle (tray checkbox); resets to cfg.Merge on restart

	// Two pipeline stages, one goroutine each. Transcription must be serialized
	// (whisper.Context is not safe for concurrent use) but must not wait for the
	// slow tail: post-processing takes seconds, and merge mode needs the NEXT
	// dictation to be transcribed while the previous one is still in the LLM.
	jobs    chan job     // recordings awaiting transcription
	texts   chan textJob // transcripts awaiting post-processing + injection
	pending atomic.Int32 // dictations in flight anywhere in the pipeline; 0 = idle

	// carry is the not-yet-injected transcript of the dictation being processed,
	// guarded by carryMu. In merge mode the next dictation appends to it and the
	// whole text is post-processed again; the in-flight run is cancelled through
	// jobCancel and its epoch goes stale, so it injects nothing. All of it is
	// cleared once the text is injected — that closes the cycle, so a dictation
	// started afterwards begins a new text.
	carryMu   sync.Mutex
	carry     string
	carryLang string
	epoch     uint64             // bumped on every merge; a job with an older epoch is superseded
	merging   int                // dictations that announced a merge but have not been transcribed yet
	jobCancel context.CancelFunc // aborts the in-flight job; nil when idle
}

// job is a finished recording handed to the worker: everything the processing
// stage needs, snapshotted under the lock so a following dictation cannot
// change it mid-flight (d.lang in particular is overwritten by the next press).
type job struct {
	samples []float32
	peak    float64
	lang    string
	byClick bool // started by a tray click → clipboard only, no paste
	merge   bool // recording began while an earlier dictation was in flight → continue its text
}

// textJob is a transcript handed from the transcribe stage to the post-process
// and injection stage. epoch identifies the merge cycle the text belongs to: a
// later dictation that merges into it bumps the epoch, and a job whose epoch is
// stale injects nothing — the merged text supersedes it.
type textJob struct {
	text          string
	lang          string
	byClick       bool
	translateMode bool
	epoch         uint64
}

// queueDepth bounds the dictation backlog. Recording is cheap, processing is
// not: a stuck hotkey must not queue an unbounded amount of work.
const queueDepth = 8

// Run starts the daemon. If the socket is already served, it exits quietly
// (one instance per socket). The socket starts listening immediately while the
// model loads in the background, so START does not wait for the load. version
// is the build version shown in the tray.
func Run(version string) error {
	prepareDaemonProcess()
	cfg := config.Load()
	sock := cfg.Socket
	if ipc.Alive(sock) { // already alive
		return nil
	}

	ln, err := ipc.Listen(sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}
	defer func() { ln.Close(); ipc.Cleanup(sock) }()

	d := &Daemon{cfg: cfg, rec: &audio.Recorder{}, enabled: true, modelReady: make(chan struct{}), version: version}
	d.postScript = cfg.PostProcess
	if cfg.PostProcess != "" {
		d.postOn.Store(cfg.PostProcessOn)
	}
	d.englishInput.Store(cfg.EnglishInput)
	d.merge.Store(cfg.Merge)

	// Two-stage pipeline: recording returns immediately, transcription and the
	// post-process/inject tail each get their own goroutine, and both keep the
	// spoken order. Closing jobs unwinds the chain (transcriber closes texts).
	d.jobs = make(chan job, queueDepth)
	d.texts = make(chan textJob, queueDepth)
	go d.transcriber()
	go d.emitter()
	defer close(d.jobs)

	// pick the input/output backend (X11 or Wayland)
	backend := platform.Detect(platform.Backend(cfg.Backend))
	fmt.Fprintf(os.Stderr, "[vole] backend: %s\n", backend)
	d.setupIO(backend)
	defer d.ind.Close()

	// dictation history (persisted) feeding the tray's "recent dictations" menu
	d.hist = history.New(cfg.HistoryFile, cfg.HistorySize)

	// system-tray icon: left-click = start/stop recording, right-click = menu
	// (recent dictations, enable/disable, auto-paste, post-process, quit). The
	// auto-paste checkbox only applies to the clipboard-paste injector; the
	// post-process checkbox only appears when a post-processing script is
	// configured.
	var onAutoPaste func()
	if _, ok := d.inj.(platform.AutoPaster); ok {
		onAutoPaste = d.toggleAutoPaste
	}
	var onPostProcess func()
	if cfg.PostProcess != "" {
		onPostProcess = d.togglePostProcess
	}
	onEnglishInput := d.toggleEnglishInput
	// merging only means anything when a post-process script can re-run on the
	// grown text; without one each dictation is injected as it is transcribed.
	var onMerge func()
	if cfg.PostProcess != "" {
		onMerge = d.toggleMerge
	}
	d.tr = tray.Run(d.toggleEnabled, func() { os.Exit(0) }, d.toggleRecording, d.pasteHistory, cfg.HistorySize, onAutoPaste, cfg.AutoPaste, onPostProcess, d.postOn.Load(), onEnglishInput, d.englishInput.Load(), onMerge, d.merge.Load(), d.version)
	d.hist.OnChange(func(entries []history.Entry) {
		texts := make([]string, len(entries))
		for i, e := range entries {
			texts[i] = e.Text
		}
		d.tr.SetHistory(texts)
	})

	// global PTT hotkey, asynchronously (the Wayland portal handshake may block)
	go d.startHotkey(backend)
	defer func() {
		d.mu.Lock()
		hk := d.hk
		d.mu.Unlock()
		if hk != nil {
			hk.Close()
		}
	}()

	// load the model in the background — the socket already accepts commands
	go func() {
		if err := d.ensureModels(); err != nil {
			fmt.Fprintln(os.Stderr, "vole daemon:", err)
			notify("🎤 vole", "model download failed")
			os.Exit(1)
		}
		ctx, err := whisper.New(cfg.Model, true, cfg.VAD, cfg.VADThreshold)
		if err != nil {
			fmt.Fprintln(os.Stderr, "vole daemon:", err)
			notify("🎤 vole", "model loading failed")
			os.Exit(1)
		}
		d.ctx = ctx
		close(d.modelReady)
		notify("🎤 vole", "large-v3 model loaded (GPU) — ready")
	}()
	defer func() {
		if d.ctx != nil {
			d.ctx.Close()
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		d.handle(conn)
	}
}

func (d *Daemon) handle(conn net.Conn) {
	defer conn.Close()
	line, _ := bufio.NewReader(conn).ReadString('\n')
	var cmd, arg string
	fmt.Sscan(line, &cmd, &arg)

	switch cmd {
	case "START":
		if arg == "" {
			arg = "ru"
		}
		d.start(arg, false)
		fmt.Fprint(conn, "OK")
	case "STOP":
		// The transcript is produced asynchronously by the worker now, so the
		// socket only acknowledges that recording stopped.
		d.stop("") // language from the current state
		fmt.Fprint(conn, "OK")
	case "PING":
		fmt.Fprint(conn, "OK")
	default:
		fmt.Fprint(conn, "ERR")
	}
}

// toggleRecording handles a tray left-click, by what the icon currently shows:
// recording (red) stops it; processing (amber/blue) cancels the dictation and
// the backlog; idle starts recording, whose result goes to the clipboard only
// (the focus is on the tray, not a text field). While dictation is disabled
// (grey) the click does nothing. It must return promptly — the tray host waits
// for the DBus reply — so cancelling only signals the worker.
func (d *Daemon) toggleRecording() {
	// One lock for the decision and the recording transition: clicks arrive on a
	// fresh DBus goroutine each, so reading the state and acting on it separately
	// would let two rapid clicks both start a recording.
	d.mu.Lock()
	if !d.enabled {
		d.mu.Unlock()
		return
	}
	rec := d.recording
	d.mu.Unlock()
	switch {
	case rec:
		d.stop("")
	case d.pending.Load() > 0:
		d.cancelProcessing()
	default:
		d.start(d.cfg.Hotkey.Lang, true)
	}
}

func (d *Daemon) start(lang string, byClick bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled || d.recording {
		return // disabled in the tray, or idempotent (key autorepeat)
	}
	if err := d.rec.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: start:", err)
		return
	}
	d.lang = lang
	d.recording = true
	d.byClick = byClick
	// Merge decision is made HERE, when recording starts: what matters is that the
	// user began speaking again while the previous dictation had not landed yet.
	// Deciding later (once this recording is transcribed) would be too late — by
	// then the previous text may already have been post-processed and injected.
	// claimMerge asks whether a cycle is still open and, if so, holds it open for
	// as long as this dictation is being spoken — speaking takes seconds, and the
	// earlier text must not land in the meantime. Asking "is a cycle open?" rather
	// than "is anything in flight?" is deliberate: the in-flight counter drops
	// just before the injection, leaving a window in which a dictation started
	// there would wrongly begin a new text.
	d.mergeNext = d.merge.Load() && d.claimMerge()

	d.ind.Show(lang)
	d.levelStop = make(chan struct{})
	go d.feedLevel(d.levelStop)
	if d.tr != nil {
		d.tr.SetState(tray.StateRecording)
	}
}

// ensureModels downloads missing model/VAD files, showing progress in the tray
// tooltip and the overlay popup. No-op if both files already exist.
func (d *Daemon) ensureModels() error {
	items := []struct{ path, label string }{
		{d.cfg.VAD, modelLabel(d.cfg.VAD)},
		{d.cfg.Model, modelLabel(d.cfg.Model)},
	}
	for _, it := range items {
		if models.Exists(it.path) {
			continue
		}
		notify("🎤 vole", "downloading "+it.label+"…")
		d.ind.ShowDownload(it.label)
		err := models.Ensure(it.path, func(done, total int64) {
			frac := 0.0
			if total > 0 {
				frac = float64(done) / float64(total)
			}
			if d.tr != nil {
				d.tr.SetTooltip(fmt.Sprintf("vole: downloading %s %d%%", it.label, int(frac*100)))
			}
			d.ind.SetProgress(frac)
		})
		d.ind.Hide()
		if d.tr != nil {
			d.tr.ResetTooltip()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// modelLabel turns ".../ggml-large-v3.bin" into "large-v3" for display.
func modelLabel(path string) string {
	name := strings.TrimPrefix(filepath.Base(path), "ggml-")
	return strings.TrimSuffix(name, ".bin")
}

// toggleEnabled enables/disables dictation (via the tray click). While disabled,
// PTT presses are ignored, but the model stays in VRAM for an instant return.
func (d *Daemon) toggleEnabled() {
	d.mu.Lock()
	d.enabled = !d.enabled
	en := d.enabled
	d.mu.Unlock()
	if d.tr != nil {
		d.tr.SetEnabled(en)
	}
	if en {
		notify("🎤 vole", "dictation enabled")
	} else {
		notify("🎤 vole", "dictation disabled")
	}
}

// toggleAutoPaste flips auto-paste at runtime (via the tray checkbox): while off,
// a dictation only lands on the clipboard instead of being pasted. In-memory —
// it resets to the configured auto_paste on restart.
func (d *Daemon) toggleAutoPaste() {
	ap, ok := d.inj.(platform.AutoPaster)
	if !ok {
		return
	}
	on := !ap.AutoPaste()
	ap.SetAutoPaste(on)
	if d.tr != nil {
		d.tr.SetAutoPaste(on)
	}
	if on {
		notify("🎤 vole", "auto-paste on")
	} else {
		notify("📋 vole", "auto-paste off — dictation copies to the clipboard")
	}
}

// togglePostProcess flips post-processing at runtime (via the tray checkbox):
// while on, each dictation is piped through the configured script (stdin) and
// the improved text (stdout) is what is pasted; the raw transcript still lands
// on the clipboard and in the history. In-memory — it resets to the configured
// postprocess_on on restart.
func (d *Daemon) togglePostProcess() {
	on := !d.postOn.Load()
	d.postOn.Store(on)
	if d.tr != nil {
		d.tr.SetPostProcess(on)
	}
	if on {
		notify("✨ vole", "post-processing on")
	} else {
		notify("vole", "post-processing off")
	}
}

// setLang changes the language of the current recording (live Shift) and the overlay label.
func (d *Daemon) setLang(lang string) {
	d.mu.Lock()
	if d.recording {
		d.lang = lang
	}
	d.mu.Unlock()
	d.ind.SetLang(lang)
}

// toggleEnglishInput flips the English-input mode at runtime (via the tray
// checkbox). It only affects the Shift/dictate-alt combo (Super+Control+Shift+D):
// while off, that combo translates Russian speech → English (the default
// translate mode); while on, it transcribes English speech → English text (with
// the repair post-process hook) instead. The base combo (Super+Control+D) is
// always Russian → Russian and is not affected. In-memory — it resets to the
// configured english_input on restart.
func (d *Daemon) toggleEnglishInput() {
	on := !d.englishInput.Load()
	d.englishInput.Store(on)
	if d.tr != nil {
		d.tr.SetEnglishInput(on)
	}
	if on {
		notify("🎤 vole", "English input on — Shift+combo transcribes English")
	} else {
		notify("🎤 vole", "English input off — Shift+combo translates to English")
	}
}

// feedLevel pumps the signal level from the recorder into the indicator while recording.
func (d *Daemon) feedLevel(stop chan struct{}) {
	t := time.NewTicker(33 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			d.ind.SetLevel(d.rec.Level())
		}
	}
}

// stop stops recording and queues the dictation for processing. lang != "" sets
// the final language (from the hotkey, honoring Shift at release time); "" keeps
// the current one. It returns as soon as the samples are captured — transcribing,
// post-processing and injection run on the worker goroutine, so the next
// dictation can start recording immediately instead of waiting behind this one.
func (d *Daemon) stop(lang string) {
	d.mu.Lock()
	if !d.recording {
		d.mu.Unlock()
		return
	}
	samples := d.rec.Stop()
	peak := d.rec.Peak()
	d.recording = false
	j := job{samples: samples, peak: peak, byClick: d.byClick, merge: d.mergeNext}
	if lang != "" {
		d.lang = lang
	}
	j.lang = d.lang
	if d.levelStop != nil {
		close(d.levelStop) // stop feeding the level
		d.levelStop = nil
	}
	d.mu.Unlock()

	// The merge was already claimed in start(), so the pending text has stayed
	// open for the whole time this dictation was being spoken.
	audioSec := float64(len(j.samples)) / float64(audio.SampleRate)
	// Debug: dump the raw capture (before the silence/VAD/transcription steps) so
	// dropped or misheard dictations can be inspected. No-op unless configured.
	d.saveRawRecording(j.samples)
	if j.peak < d.cfg.SilenceThreshold {
		fmt.Fprintf(os.Stderr, "[vole] silence: peak=%.4f, %.1fs audio — skipping\n", j.peak, audioSec)
		if j.merge {
			d.withdrawMerge() // nothing to merge after all — let the pending text land
		}
		d.setIdleIfDrained() // silence / empty press — don't inject hallucinations
		return
	}

	d.pending.Add(1)
	d.ind.SetMode(platform.ModeProcessing) // indicator: transcribing
	if d.tr != nil {
		d.tr.SetState(tray.StateProcessing)
	}
	select {
	case d.jobs <- j:
	default:
		// Backlog full: drop rather than block the hotkey path (blocking here would
		// stall the X event loop and re-create the very problem the queue fixes).
		d.pending.Add(-1)
		if j.merge {
			d.withdrawMerge()
		}
		fmt.Fprintf(os.Stderr, "[vole] queue full (%d) — dictation dropped\n", queueDepth)
		notify("🎤 vole", "queue full — dictation dropped")
		d.setIdleIfDrained()
	}
}

// transcriber drains the recording queue one job at a time — required, not
// merely convenient: whisper.Context is not safe for concurrent use. Each
// transcript is handed to the emitter stage, which is where the slow work is,
// so a following dictation is transcribed while the previous one is still being
// post-processed. That overlap is what makes merge mode possible.
func (d *Daemon) transcriber() {
	defer close(d.texts)
	for j := range d.jobs {
		tj, ok := d.transcribe(j)
		if !ok {
			d.pending.Add(-1)
			d.setIdleIfDrained()
			continue
		}
		d.texts <- tj
	}
}

// emitter post-processes and injects transcripts in the order they were spoken.
func (d *Daemon) emitter() {
	for tj := range d.texts {
		d.emit(tj)
		d.pending.Add(-1)
		d.setIdleIfDrained()
	}
}

// setIdleIfDrained returns the indicator to idle once nothing is left to do.
// With a queue the previous "always go idle when this dictation ends" would
// clear the icon while later dictations are still waiting — and, worse, would
// tear down the overlay of a recording that has meanwhile started. So: bail out
// while anything is still in flight OR a new recording is under way. Dictation
// disabled in the tray keeps its own (grey) icon — never overwrite it here.
// setModeIfNotRecording drives the overlay from the pipeline stages without
// stealing it from a dictation that is being recorded right now: the recording
// overlay (with its level meter) outranks the background progress of an earlier
// dictation. The tray icon still shows the pipeline state.
func (d *Daemon) setModeIfNotRecording(m platform.Mode) {
	d.mu.Lock()
	recording := d.recording
	d.mu.Unlock()
	if recording {
		return
	}
	d.ind.SetMode(m)
}

func (d *Daemon) setIdleIfDrained() {
	if d.pending.Load() > 0 {
		return
	}
	d.mu.Lock()
	recording, enabled := d.recording, d.enabled
	d.mu.Unlock()
	if recording {
		return // a new dictation owns the overlay now
	}
	d.ind.Hide()
	if d.tr != nil && enabled {
		d.tr.SetState(tray.StateIdle)
	}
}

// transcribe turns one queued recording into text. ok is false when there is
// nothing to pass on (transcription failed). It runs only on the transcriber
// goroutine.
func (d *Daemon) transcribe(j job) (textJob, bool) {
	samples, peak := j.samples, j.peak
	audioSec := float64(len(samples)) / float64(audio.SampleRate)

	<-d.modelReady // wait for the model to load if it is still loading
	t0 := time.Now()
	// Translate mode: the alt/shift language (dictate-alt) turns speech into
	// English. whisper's own translate task is unreliable on the turbo model, and
	// so is its language auto-detection with VAD (both return empty), so instead we
	// transcribe with the PRIMARY language pinned (the "dictate" language — e.g.
	// Russian, which is what the user actually speaks) and hand the clean source
	// transcript to the post-process LLM to translate to English. In that mode we
	// seed no initial_prompt (an English seed corrupts a non-English transcript,
	// and a stale non-English en-history entry would corrupt it too) and the
	// translate step runs the post-process script with VOLE_PP_TRANSLATE=1 — the
	// script switches from repair to translate. Otherwise seed whisper with the
	// previous same-language dictation to steady short, ambiguous phrases.
	translateMode := d.cfg.Translate && d.cfg.Hotkey.LangShift == "en" && j.lang == "en" && !d.englishInput.Load()
	transcribeLang := j.lang
	prompt := ""
	if translateMode {
		transcribeLang = d.cfg.Hotkey.Lang // pin the primary language (auto-detect returns empty on the turbo model)
	} else {
		prompt = d.whisperPrompt(j.lang)
	}
	gain := audio.NormalizePeak(samples, 0.8, 64)
	text, err := d.ctx.Transcribe(samples, transcribeLang, threads, prompt)
	dur := time.Since(t0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: transcribe:", err)
		if j.merge {
			d.withdrawMerge() // this dictation will never merge — release the pending text
		}
		return textJob{}, false
	}
	if cleaned := whisper.ScrubHallucination(text); cleaned != text {
		if cleaned == "" {
			fmt.Fprintf(os.Stderr, "[vole] dropped whisper hallucination: %q\n", text)
			if j.merge {
				d.withdrawMerge()
			}
			return textJob{}, false
		}
		text = cleaned
	}
	fmt.Fprintf(os.Stderr, "[vole] %s | audio %.1fs | transcribed in %.2fs | peak=%.3f gain=%.1f | %q\n",
		j.lang, audioSec, dur.Seconds(), peak, gain, text)
	// Merge mode: this dictation continues one whose text has not landed yet —
	// append to the pending raw transcript so the whole thing is post-processed
	// again. This happens here, in the transcribe stage, because that is where we
	// still overlap with the previous dictation's (slow) post-processing: the
	// superseded run is cancelled and its epoch goes stale, so only the merged
	// text is ever injected. Carry is cleared after injection, which ends the
	// cycle: a dictation started after that begins a new text.
	text, epoch := d.mergeCarry(text, j.lang, j.merge)
	return textJob{text: text, lang: j.lang, byClick: j.byClick, translateMode: translateMode, epoch: epoch}, true
}

// emit post-processes one transcript and injects the result. It runs only on the
// emitter goroutine, so injections keep the order in which they were spoken.
func (d *Daemon) emit(tj textJob) {
	text, byClick, translateMode := tj.text, tj.byClick, tj.translateMode
	// In translate mode the raw transcript is the source language (e.g. Russian)
	// but tj.lang is "en"; keep it out of the en-history (only the English result
	// goes in) to avoid polluting the same-language prompt seed and the menu.
	if !translateMode {
		d.hist.Add(text, tj.lang) // record the raw dictation (tray menu + history file)
	}
	// Optional post-processing: pipe the raw transcript through a script and use
	// its stdout as the improved text. On any error or empty output we fall back
	// to the raw transcript, so a broken script never blocks dictation. In
	// translate mode the script is the translator (VOLE_PP_TRANSLATE=1), so it
	// runs regardless of the post-process toggle — translate needs it. While the
	// script runs, switch the indicator to a distinct "post" state (a blue tray
	// dot / blue overlay icon) so the user sees the hook is active even though
	// recognition is finished — then drop it before injection so a clipboard toast
	// (confirmCopied) behaves as on the non-post path.
	postActive := text != "" && d.postScript != "" && (translateMode || d.postOn.Load())
	if postActive {
		if d.tr != nil {
			d.tr.SetState(tray.StatePostProcessing)
		}
		d.setModeIfNotRecording(platform.ModePostProcessing)
	}
	// The run is cancellable: a tray click aborts it, and in merge mode the next
	// dictation abandons it in favour of the merged text.
	ctx, cancel := d.beginCancellable()
	var improved string
	postOK := false
	var postErr error
	if translateMode {
		// translate needs the LLM regardless of the post-process toggle
		improved, postOK, postErr = d.postProcess(ctx, text, []string{"VOLE_PP_TRANSLATE=1"}, true)
	} else {
		improved, postOK, postErr = d.postProcess(ctx, text, nil, false)
	}
	cancel()
	d.endCancellable()
	// Abandoned: either the user cancelled, or a merging dictation superseded this
	// text. Either way nothing is injected — the merged run does the insertion.
	if errors.Is(postErr, postprocess.ErrCanceled) {
		fmt.Fprintln(os.Stderr, "[vole] post-processing canceled")
		return
	}
	// A merge can also land while the script was starting or already finished;
	// the epoch check covers those windows, where no cancellation was observed.
	// Claiming the cycle here — atomically, under the same lock a merge takes —
	// closes the last gap: a merge arriving after this point finds the cycle
	// already closed and starts a new text instead of racing this injection.
	if !d.commitCarry(tj.epoch) {
		fmt.Fprintln(os.Stderr, "[vole] superseded by a merged dictation — not injecting")
		return
	}
	if postOK {
		d.hist.Add(improved, tj.lang) // improved text as a separate, newest history entry
	}
	// tray-click dictation only copies to the clipboard (focus is on the tray);
	// PTT / socket dictation injects into the focused window — unless auto-paste
	// is toggled off, when Type() itself only copies (we then confirm it too).
	// With post-processing on and a clipboard-history backend, the raw transcript
	// is seeded first (so it stays as the previous Klipper entry) and the improved
	// text is pasted; both also stay in the history.
	var injErr error
	copier, hasCopier := d.inj.(platform.Copier)
	switch {
	case hasCopier && byClick:
		injErr = d.copyTwo(copier, text, improved, postOK) // clipboard only, no paste
		if injErr == nil {
			d.confirmCopied()
		}
	case postOK:
		if hasCopier {
			_ = copier.CopyHistory(text) // raw as the previous clipboard-history entry; no-op without a history
		}
		if ap, ok := d.inj.(platform.AutoPaster); ok && !ap.AutoPaste() {
			if hasCopier {
				injErr = copier.Copy(improved) // auto-paste off: improved on clipboard, no paste
			} else {
				injErr = d.inj.Type(improved)
			}
			if injErr == nil {
				d.confirmCopied()
			}
		} else if ins, ok := d.inj.(platform.Inserter); ok {
			injErr = ins.Insert(improved) // paste the improved text into the focused window
		} else {
			injErr = d.inj.Type(improved) // degraded: no clipboard, just type improved
		}
	default:
		injErr = d.inj.Type(text)
		if ap, ok := d.inj.(platform.AutoPaster); ok && !ap.AutoPaste() && injErr == nil {
			d.confirmCopied()
		}
	}
	if injErr != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: inject:", injErr)
	}
	// The merge cycle was already closed by commitCarry above, so a dictation
	// started from here on begins a new text.
}

// postProcess runs the configured script on the raw transcript, returning the
// improved text. env adds extra environment variables for the script (used in
// translate mode to pass VOLE_PP_TRANSLATE=1). force runs the script even when
// the post-process toggle is off — used in translate mode, where the script is
// the translator, not an optional repair. It returns ("", false, nil) when no
// script is configured, the input is empty, the toggle is off (and force is
// false), or the script fails — in all these cases the caller uses the raw text.
// Cancelling ctx kills the script and returns postprocess.ErrCanceled as the
// third value, which tells the caller to inject nothing at all.
func (d *Daemon) postProcess(ctx context.Context, text string, env []string, force bool) (string, bool, error) {
	if text == "" || d.postScript == "" {
		return "", false, nil
	}
	if !force && !d.postOn.Load() {
		return "", false, nil
	}
	to := time.Duration(d.cfg.PostProcessTimeout) * time.Second
	if to <= 0 {
		to = 30 * time.Second
	}
	improved, err := postprocess.RunContext(ctx, d.postScript, text, to, env)
	if err != nil {
		if errors.Is(err, postprocess.ErrCanceled) {
			return "", false, err
		}
		fmt.Fprintln(os.Stderr, "vole daemon:", err)
		return "", false, nil
	}
	fmt.Fprintf(os.Stderr, "[vole] post-processed: %q -> %q\n", text, improved)
	return improved, true, nil
}

// mergeCarry folds text into the dictation currently being processed and returns
// the text to work with. In merge mode a dictation started before the previous
// one landed is a continuation: the new raw transcript is appended to the
// pending one (separated by a space) and the whole text goes through
// post-processing again, while the superseded run is cancelled so only the
// merged result is injected. With merge off — or when nothing is pending — the
// text is simply recorded as the new carry and returned unchanged.
// claimMerge reports whether a merge cycle is open — a transcript is waiting to
// be injected — and, when it is, holds it open until this dictation has been
// folded in. It runs when recording STARTS: a dictation takes seconds to speak,
// and without the claim the earlier text would be post-processed and injected
// while the user is still talking, so the continuation would land as a separate
// insertion.
//
// A cycle is open while anything is still in the pipeline (pending), while a
// transcript is waiting to be injected (carry), or while another dictation has
// already claimed it (merging) — the first covers the stretch before this
// dictation has even been transcribed, when there is no carry yet.
func (d *Daemon) claimMerge() bool {
	d.carryMu.Lock()
	defer d.carryMu.Unlock()
	if d.pending.Load() == 0 && d.carry == "" && d.merging == 0 {
		return false // nothing pending — this dictation starts a new text
	}
	d.merging++
	return true
}

// withdrawMerge undoes claimMerge for a dictation that never reached the merge
// step (silence, a failed transcription, a dropped job), releasing the pending
// text so it can be injected.
func (d *Daemon) withdrawMerge() {
	d.carryMu.Lock()
	if d.merging > 0 {
		d.merging--
	}
	d.carryMu.Unlock()
}

func (d *Daemon) mergeCarry(text, lang string, wantMerge bool) (string, uint64) {
	d.carryMu.Lock()
	defer d.carryMu.Unlock()
	if wantMerge && d.merging > 0 {
		d.merging--
	}
	d.epoch++
	if wantMerge && d.carry != "" && d.carryLang == lang {
		if d.jobCancel != nil {
			d.jobCancel() // abandon the in-flight run; the merged text supersedes it
		}
		merged := strings.TrimSpace(d.carry) + " " + strings.TrimSpace(text)
		d.carry = merged
		fmt.Fprintf(os.Stderr, "[vole] merged with pending text: %q\n", merged)
		return merged, d.epoch
	}
	d.carry = text
	d.carryLang = lang
	return text, d.epoch
}

// commitCarry claims the right to inject this text and ends the merge cycle in
// one atomic step. It returns false when the text is obsolete: either a later
// dictation already merged into it, or one is on its way (announced at the end
// of its recording, not yet transcribed) — in both cases the merged run does the
// injection, and injecting here too would duplicate the beginning.
//
// Closing the cycle here rather than after the injection is what makes it
// atomic: a merge arriving mid-injection finds an empty carry and starts a new
// text instead of producing a second, overlapping insertion.
func (d *Daemon) commitCarry(epoch uint64) bool {
	d.carryMu.Lock()
	defer d.carryMu.Unlock()
	if epoch != d.epoch || d.merging > 0 {
		return false
	}
	d.carry = ""
	d.carryLang = ""
	return true
}

// beginCancellable publishes a cancel func for the run that is about to start,
// so a tray click or a merging dictation can abort it.
func (d *Daemon) beginCancellable() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	d.carryMu.Lock()
	d.jobCancel = cancel
	d.carryMu.Unlock()
	return ctx, cancel
}

// endCancellable withdraws the cancel func once the run is over.
func (d *Daemon) endCancellable() {
	d.carryMu.Lock()
	d.jobCancel = nil
	d.carryMu.Unlock()
}

// cancelProcessing aborts the in-flight dictation and drops everything queued
// behind it (the tray-icon click while the icon is not grey). It only signals —
// the worker unwinds on its own — so the DBus tray handler returns immediately.
func (d *Daemon) cancelProcessing() {
	// Drop the backlog first, so the worker does not pick up the next job.
	for {
		select {
		case <-d.jobs:
			d.pending.Add(-1)
			continue
		default:
		}
		break
	}
	d.carryMu.Lock()
	cancel := d.jobCancel
	d.carry = ""
	d.carryLang = ""
	d.merging = 0 // the announced merges died with the backlog
	d.epoch++     // invalidate anything already past its cancellation point
	d.carryMu.Unlock()
	if cancel != nil {
		cancel()
	}
	fmt.Fprintln(os.Stderr, "[vole] processing canceled by the user")
	notifyCanceled(d)
	d.setIdleIfDrained()
}

// notifyCanceled reports a cancellation the same way confirmCopied does: an
// overlay toast on X11, a desktop notification where there is no overlay.
func notifyCanceled(d *Daemon) {
	d.ind.Toast("Canceled")
	if _, nop := d.ind.(platform.Nop); nop {
		notify("🎤 vole", "canceled")
	}
}

// toggleMerge flips merge mode at runtime (via the tray checkbox). In-memory —
// it resets to the configured merge on restart.
func (d *Daemon) toggleMerge() {
	on := !d.merge.Load()
	d.merge.Store(on)
	if d.tr != nil {
		d.tr.SetMerge(on)
	}
	if on {
		notify("🎤 vole", "merge on — dictating again continues the pending text")
	} else {
		notify("🎤 vole", "merge off — each dictation is separate")
	}
}

// whisperPrompt returns a short initial_prompt for whisper — the most recent
// prior dictation in the SAME language, trimmed — when whisper_prompt is
// enabled; "" otherwise. Seeding across languages corrupts the output script
// (a Cyrillic prompt makes an English dictation come out transliterated), so
// only same-language entries qualify. It uses the history *before* the current
// dictation is recorded, so it never seeds the decoder with the very text it is
// about to transcribe. Entries without a recorded language (e.g. from before
// this field existed) are skipped, since their language is unknown.
func (d *Daemon) whisperPrompt(lang string) string {
	if !d.cfg.WhisperPrompt || d.hist == nil || lang == "" {
		return ""
	}
	for _, e := range d.hist.Items() { // newest first
		if e.Lang != lang {
			continue
		}
		p := strings.TrimSpace(e.Text)
		if p == "" || whisper.ScrubHallucination(p) == "" {
			continue
		}
		// one prior utterance, capped to a sentence-ish length: whisper's
		// initial_prompt is a token seed, not a context window.
		if r := []rune(p); len(r) > 200 {
			p = string(r[:200])
		}
		return p
	}
	return ""
}

// copyTwo puts raw (and, when postOK, improved) on the clipboard without pasting
// — used for tray-click dictation, where the focus is on the tray, not a field.
// improved becomes the active selection and raw only seeds the previous
// clipboard-history entry (a no-op on backends without a history). Without
// post-processing, raw is itself the dictation and is copied for real.
func (d *Daemon) copyTwo(c platform.Copier, raw, improved string, postOK bool) error {
	if !postOK {
		return c.Copy(raw)
	}
	if err := c.CopyHistory(raw); err != nil {
		return err
	}
	return c.Copy(improved)
}

// confirmCopied gives feedback that text landed on the clipboard without being
// pasted (tray-click dictation, or PTT with auto-paste off): an overlay toast on
// X11/Windows, a desktop notification where there is no overlay (Wayland tray-only).
func (d *Daemon) confirmCopied() {
	d.ind.Toast("Copied to clipboard")
	if _, nop := d.ind.(platform.Nop); nop {
		notify("📋 vole", "Copied to clipboard")
	}
}

// saveRawRecording writes the raw capture to a timestamped WAV beside the
// configured debug_record path and prunes the set to the newest debug_record_keep
// files. No-op when debug_record is empty. Best-effort: errors only log.
func (d *Daemon) saveRawRecording(samples []float32) {
	base := d.cfg.DebugRecord
	if base == "" {
		return
	}
	keep := d.cfg.DebugRecordKeep
	if keep < 1 {
		keep = 1
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if ext == "" {
		ext = ".wav"
	}
	// Millisecond resolution (Go needs a dot for fractional seconds) keeps names
	// unique for rapid dictations and lexically sortable by time.
	path := stem + "-" + time.Now().Format("20060102-150405.000") + ext
	if err := audio.WriteWAV16(path, samples); err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: record:", err)
		return
	}
	audioSec := float64(len(samples)) / float64(audio.SampleRate)
	fmt.Fprintf(os.Stderr, "[vole] raw recording saved: %s (%.1fs)\n", path, audioSec)

	// Prune oldest: the fixed-width timestamp makes lexical order chronological.
	matches, _ := filepath.Glob(stem + "-*" + ext)
	if len(matches) > keep {
		sort.Strings(matches)
		for _, old := range matches[:len(matches)-keep] {
			_ = os.Remove(old)
		}
	}
}

// pasteHistory inserts the recent dictation at idx (clicked in the tray menu)
// into the focused window, always inserting regardless of the auto-paste setting.
func (d *Daemon) pasteHistory(idx int) {
	if d.hist == nil {
		return
	}
	items := d.hist.Items()
	if idx < 0 || idx >= len(items) {
		return
	}
	text := items[idx].Text
	time.Sleep(250 * time.Millisecond) // let the tray menu close and focus return
	var err error
	if ins, ok := d.inj.(platform.Inserter); ok {
		err = ins.Insert(text)
	} else {
		err = d.inj.Type(text)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: paste history:", err)
	}
}
