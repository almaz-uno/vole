// Package daemon is vole's resident server: it keeps the whisper model in VRAM,
// listens on a unix socket, and records and transcribes audio on command.
package daemon

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/almaz-uno/vole/internal/audio"
	"github.com/almaz-uno/vole/internal/config"
	"github.com/almaz-uno/vole/internal/history"
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
	enabled   bool
	lang      string

	postOn     atomic.Bool // post-processing runtime toggle (tray checkbox); resets to cfg.PostProcessOn on restart
	postScript string      // configured post-processing script path; empty = feature unavailable

	ctx        *whisper.Context
	modelReady chan struct{} // closed once the model is loaded

	ind       platform.Indicator // overlay (X11) or Nop (Wayland tray-only); never nil
	inj       platform.Injector  // text injection backend; never nil
	hk        platform.Hotkey    // PTT source; nil if unavailable
	tr        *tray.Tray
	hist      *history.Store // recent dictations (tray menu + persisted file)
	levelStop chan struct{}  // stops the goroutine feeding the level to the indicator
}

// Run starts the daemon. If the socket is already served, it exits quietly
// (one instance per socket). The socket starts listening immediately while the
// model loads in the background, so START does not wait for the load. version
// is the build version shown in the tray.
func Run(version string) error {
	cfg := config.Load()
	sock := cfg.Socket
	if c, err := net.Dial("unix", sock); err == nil { // already alive
		c.Close()
		return nil
	}
	_ = os.Remove(sock) // clean up a stale socket

	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}
	defer func() { ln.Close(); os.Remove(sock) }()

	d := &Daemon{cfg: cfg, rec: &audio.Recorder{}, enabled: true, modelReady: make(chan struct{}), version: version}
	d.postScript = cfg.PostProcess
	if cfg.PostProcess != "" {
		d.postOn.Store(cfg.PostProcessOn)
	}

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
	d.tr = tray.Run(d.toggleEnabled, func() { os.Exit(0) }, d.toggleRecording, d.pasteHistory, cfg.HistorySize, onAutoPaste, cfg.AutoPaste, onPostProcess, d.postOn.Load(), d.version)
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
		fmt.Fprint(conn, d.stop("")) // language from the current state
	case "PING":
		fmt.Fprint(conn, "OK")
	default:
		fmt.Fprint(conn, "ERR")
	}
}

// toggleRecording starts or stops recording from a tray left-click. The result
// goes to the clipboard only (the focus is on the tray, not a text field).
func (d *Daemon) toggleRecording() {
	d.mu.Lock()
	rec := d.recording
	d.mu.Unlock()
	if rec {
		d.stop("")
	} else {
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

// stop stops recording and transcribes. lang != "" sets the final language
// (from the hotkey, honoring Shift at release time); "" keeps the current one.
func (d *Daemon) stop(lang string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.recording {
		return ""
	}
	samples := d.rec.Stop()
	peak := d.rec.Peak()
	d.recording = false
	byClick := d.byClick // tray-click dictation → clipboard only
	if lang != "" {
		d.lang = lang
	}
	if d.levelStop != nil {
		close(d.levelStop) // stop feeding the level
		d.levelStop = nil
	}

	audioSec := float64(len(samples)) / float64(audio.SampleRate)
	// Debug: dump the raw capture (before the silence/VAD/transcription steps) so
	// dropped or misheard dictations can be inspected. No-op unless configured.
	d.saveRawRecording(samples)
	if peak < d.cfg.SilenceThreshold {
		fmt.Fprintf(os.Stderr, "[vole] silence: peak=%.4f, %.1fs audio — skipping\n", peak, audioSec)
		d.ind.Hide()
		if d.tr != nil {
			d.tr.SetState(tray.StateIdle)
		}
		return "" // silence / empty press — don't inject hallucinations
	}

	d.ind.SetMode(platform.ModeProcessing) // indicator: transcribing
	if d.tr != nil {
		d.tr.SetState(tray.StateProcessing)
	}

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
	translateMode := d.cfg.Translate && d.cfg.Hotkey.LangShift == "en" && d.lang == "en"
	transcribeLang := d.lang
	prompt := ""
	if translateMode {
		transcribeLang = d.cfg.Hotkey.Lang // pin the primary language (auto-detect returns empty on the turbo model)
	} else {
		prompt = d.whisperPrompt(d.lang)
	}
	text, err := d.ctx.Transcribe(samples, transcribeLang, threads, prompt)
	dur := time.Since(t0)
	if err != nil {
		d.ind.Hide()
		if d.tr != nil {
			d.tr.SetState(tray.StateIdle)
		}
		fmt.Fprintln(os.Stderr, "vole daemon: transcribe:", err)
		return ""
	}
	fmt.Fprintf(os.Stderr, "[vole] %s | audio %.1fs | transcribed in %.2fs | peak=%.3f | %q\n",
		d.lang, audioSec, dur.Seconds(), peak, text)
	// In translate mode the raw transcript is the source language (e.g. Russian)
	// but d.lang is "en"; keep it out of the en-history (only the English result
	// goes in) to avoid polluting the same-language prompt seed and the menu.
	if !translateMode {
		d.hist.Add(text, d.lang) // record the raw dictation (tray menu + history file)
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
		d.ind.SetMode(platform.ModePostProcessing)
	} else {
		d.ind.Hide()
		if d.tr != nil {
			d.tr.SetState(tray.StateIdle)
		}
	}
	var improved string
	postOK := false
	if translateMode {
		// translate needs the LLM regardless of the post-process toggle
		improved, postOK = d.postProcess(text, []string{"VOLE_PP_TRANSLATE=1"}, true)
	} else {
		improved, postOK = d.postProcess(text, nil, false)
	}
	if postOK {
		d.hist.Add(improved, d.lang) // improved text as a separate, newest history entry
	}
	if postActive {
		d.ind.Hide()
		if d.tr != nil {
			d.tr.SetState(tray.StateIdle)
		}
	}
	// tray-click dictation only copies to the clipboard (focus is on the tray);
	// PTT / socket dictation injects into the focused window — unless auto-paste
	// is toggled off, when Type() itself only copies (we then confirm it too).
	// With post-processing on and a clipboard backend, the raw transcript is put
	// on the clipboard first (preserving it as the previous Klipper entry) and the
	// improved text is pasted; both also stay in the history.
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
			_ = copier.Copy(text) // raw → clipboard (previous Klipper entry), no paste
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
	return text
}

// postProcess runs the configured script on the raw transcript, returning the
// improved text. env adds extra environment variables for the script (used in
// translate mode to pass VOLE_PP_TRANSLATE=1). force runs the script even when
// the post-process toggle is off — used in translate mode, where the script is
// the translator, not an optional repair. It returns ("", false) when no script
// is configured, the input is empty, the toggle is off (and force is false), or
// the script fails — in all these cases the caller uses the raw text.
func (d *Daemon) postProcess(text string, env []string, force bool) (string, bool) {
	if text == "" || d.postScript == "" {
		return "", false
	}
	if !force && !d.postOn.Load() {
		return "", false
	}
	to := time.Duration(d.cfg.PostProcessTimeout) * time.Second
	if to <= 0 {
		to = 30 * time.Second
	}
	improved, err := postprocess.RunTimeoutEnv(d.postScript, text, to, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon:", err)
		return "", false
	}
	fmt.Fprintf(os.Stderr, "[vole] post-processed: %q -> %q\n", text, improved)
	return improved, true
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
		if p == "" {
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
// The last copy wins, so improved becomes the active selection; raw stays as the
// previous Klipper entry. The last error is returned.
func (d *Daemon) copyTwo(c platform.Copier, raw, improved string, postOK bool) error {
	if err := c.Copy(raw); err != nil {
		return err
	}
	if postOK {
		return c.Copy(improved)
	}
	return nil
}

// confirmCopied gives feedback that text landed on the clipboard without being
// pasted (tray-click dictation, or PTT with auto-paste off): an overlay toast on
// X11, a desktop notification where there is no overlay (Wayland tray-only).
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

func notify(title, body string) {
	_ = exec.Command("notify-send", "-t", "2500", title, body).Run()
}
