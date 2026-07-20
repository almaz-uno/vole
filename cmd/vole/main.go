// Command vole is an offline voice dictation tool backed by whisper.cpp
// (GPU/Vulkan).
//
// Usage:
//
//	vole daemon              run the resident daemon (model in VRAM)
//	vole start [ru|en|auto]  start recording (lazily spawns the daemon)
//	vole stop                stop, transcribe and inject the text
//	vole transcribe <wav>    one-shot transcription of a file (debug)
package main

import (
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/almaz-uno/vole/internal/audio"
	"github.com/almaz-uno/vole/internal/config"
	"github.com/almaz-uno/vole/internal/daemon"
	"github.com/almaz-uno/vole/internal/inject"
	"github.com/almaz-uno/vole/internal/models"
	"github.com/almaz-uno/vole/internal/overlay"
	"github.com/almaz-uno/vole/internal/whisper"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Println("vole", version)
	case "daemon":
		if err := daemon.Run(version); err != nil {
			fatal(err)
		}
	case "start":
		lang := "ru"
		if len(os.Args) > 2 {
			lang = os.Args[2]
		}
		sock := config.Load().Socket
		ensureDaemon(sock)
		if _, err := send(sock, "START "+lang); err != nil {
			fatal(err)
		}
	case "stop":
		out, err := send(config.Load().Socket, "STOP")
		if err != nil {
			fatal(err) // no daemon — nothing to stop
		}
		fmt.Print(out) // transcribed text (the daemon already injected it)
	case "download":
		cmdDownload()
	case "transcribe":
		cmdTranscribe(os.Args[2:])
	case "overlay-test":
		cmdOverlayTest()
	case "paste":
		cmdPaste(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "vole: unknown command %q\n", os.Args[1])
		usage()
	}
}

// send sends a single command to the daemon and returns its reply.
func send(sock, cmd string) (string, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	fmt.Fprintln(conn, cmd)
	b, _ := io.ReadAll(conn)
	return string(b), nil
}

// ensureDaemon spawns the daemon if it is not already listening, then waits
// until the socket accepts connections (the daemon listens before loading the
// model, so this is fast).
func ensureDaemon(sock string) {
	if c, err := net.Dial("unix", sock); err == nil {
		c.Close()
		return
	}
	exe, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	cmd := exec.Command(exe, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from the client
	devnull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if devnull != nil {
		cmd.Stdin, cmd.Stdout = devnull, devnull
		// keep stderr for model-loading diagnostics
	}
	if err := cmd.Start(); err != nil {
		fatal(fmt.Errorf("failed to start daemon: %w", err))
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	fatal(fmt.Errorf("daemon did not come up in time"))
}

// cmdDownload fetches missing model/VAD files from HuggingFace with progress.
func cmdDownload() {
	cfg := config.Load()
	for _, path := range []string{cfg.VAD, cfg.Model} {
		name := filepath.Base(path)
		if models.Exists(path) {
			fmt.Printf("%s: already present\n", name)
			continue
		}
		fmt.Printf("downloading %s\n", name)
		err := models.Ensure(path, func(done, total int64) {
			if total > 0 {
				fmt.Printf("\r  %3d%%  %.0f/%.0f MB", int(float64(done)/float64(total)*100),
					float64(done)/1e6, float64(total)/1e6)
			} else {
				fmt.Printf("\r  %.0f MB", float64(done)/1e6)
			}
		})
		fmt.Println()
		if err != nil {
			fatal(err)
		}
		fmt.Printf("%s: done\n", name)
	}
}

// cmdTranscribe performs a one-shot WAV transcription without the daemon (debug).
func cmdTranscribe(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: vole transcribe <wav> [ru|en|auto]")
		os.Exit(1)
	}
	lang := "auto"
	if len(args) > 1 {
		lang = args[1]
	}
	samples, err := audio.ReadWAV16(args[0])
	if err != nil {
		fatal(err)
	}
	cfg := config.Load()
	ctx, err := whisper.New(cfg.Model, true, cfg.VAD, cfg.VADThreshold)
	if err != nil {
		fatal(err)
	}
	defer ctx.Close()
	text, err := ctx.Transcribe(samples, lang, runtime.NumCPU(), "")
	if err != nil {
		fatal(err)
	}
	fmt.Print(text)
}

// cmdOverlayTest exercises the overlay in isolation, without recording or transcription.
func cmdOverlayTest() {
	ov, err := overlay.New()
	if err != nil {
		fatal(err)
	}
	defer ov.Close()

	ov.Show("RU")
	start := time.Now()
	for time.Since(start) < 4*time.Second {
		t := time.Since(start).Seconds()
		ov.SetLevel(0.02 + 0.13*(0.5+0.5*math.Sin(t*6))) // simulated speech level
		if t > 2.5 {
			ov.SetMode(overlay.ModeProcessing)
		}
		time.Sleep(40 * time.Millisecond)
	}
	ov.Hide()
	time.Sleep(300 * time.Millisecond)
}

// cmdPaste copies text to the clipboard and pastes it via the configured paste
// keystroke (the inject=paste path), after a short delay so you can focus the
// target field. Text comes from the args or stdin. Verification helper.
func cmdPaste(args []string) {
	text := strings.TrimRight(strings.Join(args, " "), "\n")
	if text == "" {
		b, _ := io.ReadAll(os.Stdin)
		text = strings.TrimRight(string(b), "\n")
	}
	if text == "" {
		fmt.Fprintln(os.Stderr, "usage: vole paste <text>   (or pipe text on stdin)")
		os.Exit(1)
	}
	p := inject.NewPaste(config.Load().PasteKey, true) // debug helper always pastes
	const delay = 2 * time.Second
	fmt.Fprintf(os.Stderr, "focus the target field — pasting in %s…\n", delay)
	time.Sleep(delay)
	if err := p.Type(text); err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: vole <command> [args]

Commands:
  daemon              resident daemon (model in VRAM)
  start [ru|en|auto]  start recording (lazily spawns the daemon)
  stop                stop, transcribe and inject the text
  download            download missing models (model + VAD)
  transcribe <wav>    one-shot transcription of a file (debug)`)
	os.Exit(1)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "vole:", err)
	os.Exit(1)
}
