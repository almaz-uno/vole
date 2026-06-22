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
	"strings"
	"sync"
	"time"

	"github.com/almaz-uno/vole/internal/audio"
	"github.com/almaz-uno/vole/internal/config"
	"github.com/almaz-uno/vole/internal/hotkey"
	"github.com/almaz-uno/vole/internal/inject"
	"github.com/almaz-uno/vole/internal/models"
	"github.com/almaz-uno/vole/internal/overlay"
	"github.com/almaz-uno/vole/internal/tray"
	"github.com/almaz-uno/vole/internal/whisper"
)

const threads = 8 // CPU threads for the non-GPU parts (mel, sampling)

// Daemon holds the dictation service state.
type Daemon struct {
	cfg config.Config

	mu        sync.Mutex
	rec       *audio.Recorder
	recording bool
	enabled   bool
	lang      string

	ctx        *whisper.Context
	modelReady chan struct{} // closed once the model is loaded

	ov        *overlay.Overlay
	tr        *tray.Tray
	levelStop chan struct{} // stops the goroutine feeding the level to the overlay
}

// Run starts the daemon. If the socket is already served, it exits quietly
// (one instance per socket). The socket starts listening immediately while the
// model loads in the background, so START does not wait for the load.
func Run() error {
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

	d := &Daemon{cfg: cfg, rec: &audio.Recorder{}, enabled: true, modelReady: make(chan struct{})}

	// overlay near the cursor; if X is unavailable, run without it
	if ov, err := overlay.New(); err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: overlay unavailable:", err)
	} else {
		d.ov = ov
		defer d.ov.Close()
	}

	// system-tray icon: state by color, toggle on/off, quit
	d.tr = tray.Run(d.toggleEnabled, func() { os.Exit(0) })

	// global PTT hotkey — the daemon grabs it itself (reliable release + live Shift)
	if g, err := hotkey.New(hotkey.Config{
		Mods:      cfg.Hotkey.Mods,
		Key:       cfg.Hotkey.Key,
		LangBase:  cfg.Hotkey.Lang,
		LangShift: cfg.Hotkey.LangShift,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: hotkey:", err)
	} else {
		defer g.Close()
		go g.Listen(
			func(lang string) { d.start(lang) },   // PTT press
			func(lang string) { d.setLang(lang) }, // live Shift
			func(lang string) { d.stop(lang) },    // release (final language)
		)
	}

	// load the model in the background — the socket already accepts commands
	go func() {
		if err := d.ensureModels(); err != nil {
			fmt.Fprintln(os.Stderr, "vole daemon:", err)
			notify("🎤 vole", "model download failed")
			os.Exit(1)
		}
		ctx, err := whisper.New(cfg.Model, true, cfg.VAD)
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
		d.start(arg)
		fmt.Fprint(conn, "OK")
	case "STOP":
		fmt.Fprint(conn, d.stop("")) // language from the current state
	case "PING":
		fmt.Fprint(conn, "OK")
	default:
		fmt.Fprint(conn, "ERR")
	}
}

func (d *Daemon) start(lang string) {
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

	if d.ov != nil {
		d.ov.Show(lang)
		d.levelStop = make(chan struct{})
		go d.feedLevel(d.levelStop)
	}
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
		if d.ov != nil {
			d.ov.ShowDownload(it.label)
		}
		err := models.Ensure(it.path, func(done, total int64) {
			frac := 0.0
			if total > 0 {
				frac = float64(done) / float64(total)
			}
			if d.tr != nil {
				d.tr.SetTooltip(fmt.Sprintf("vole: downloading %s %d%%", it.label, int(frac*100)))
			}
			if d.ov != nil {
				d.ov.SetProgress(frac)
			}
		})
		if d.ov != nil {
			d.ov.Hide()
		}
		if d.tr != nil {
			d.tr.SetTooltip("vole — voice dictation")
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

// setLang changes the language of the current recording (live Shift) and the overlay label.
func (d *Daemon) setLang(lang string) {
	d.mu.Lock()
	if d.recording {
		d.lang = lang
	}
	d.mu.Unlock()
	if d.ov != nil {
		d.ov.SetLang(lang)
	}
}

// feedLevel pumps the signal level from the recorder into the overlay while recording.
func (d *Daemon) feedLevel(stop chan struct{}) {
	t := time.NewTicker(33 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			d.ov.SetLevel(d.rec.Level())
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
	if lang != "" {
		d.lang = lang
	}
	if d.levelStop != nil {
		close(d.levelStop) // stop feeding the level
		d.levelStop = nil
	}

	audioSec := float64(len(samples)) / float64(audio.SampleRate)
	if peak < d.cfg.SilenceThreshold {
		fmt.Fprintf(os.Stderr, "[vole] silence: peak=%.4f, %.1fs audio — skipping\n", peak, audioSec)
		if d.ov != nil {
			d.ov.Hide()
		}
		if d.tr != nil {
			d.tr.SetState(tray.StateIdle)
		}
		return "" // silence / empty press — don't inject hallucinations
	}

	if d.ov != nil {
		d.ov.SetMode(overlay.ModeProcessing) // overlay: transcribing
	}
	if d.tr != nil {
		d.tr.SetState(tray.StateProcessing)
	}

	<-d.modelReady // wait for the model to load if it is still loading
	t0 := time.Now()
	text, err := d.ctx.Transcribe(samples, d.lang, threads)
	dur := time.Since(t0)
	if d.ov != nil {
		d.ov.Hide()
	}
	if d.tr != nil {
		d.tr.SetState(tray.StateIdle)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: transcribe:", err)
		return ""
	}
	fmt.Fprintf(os.Stderr, "[vole] %s | audio %.1fs | transcribed in %.2fs | peak=%.3f | %q\n",
		d.lang, audioSec, dur.Seconds(), peak, text)
	if err := inject.Type(text); err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: inject:", err)
	}
	return text
}

func notify(title, body string) {
	_ = exec.Command("notify-send", "-t", "2500", title, body).Run()
}
