// Package config holds vole's configuration: defaults, the
// ~/.config/vole/config.yaml file, and per-setting environment overrides.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Hotkey describes the PTT binding with Shift-based language switching.
type Hotkey struct {
	Mods      string `yaml:"mods"`       // "Super+Control"
	Key       string `yaml:"key"`        // "d"
	Lang      string `yaml:"lang"`       // language without Shift
	LangShift string `yaml:"lang_shift"` // language while Shift is held
}

// Config is the full daemon configuration.
type Config struct {
	Hotkey           Hotkey  `yaml:"hotkey"`
	SilenceThreshold float64 `yaml:"silence_threshold"`
	VADThreshold     float64 `yaml:"vad_threshold"` // Silero speech probability [0,1]; lower = catches quieter/whispered speech
	Model            string  `yaml:"model"`
	VAD              string  `yaml:"vad"`
	Socket           string  `yaml:"socket"`
	Backend          string  `yaml:"backend"`           // input/output backend: auto|x11|wayland
	Inject           string  `yaml:"inject"`            // injection method: auto|type|paste
	PasteKey         string  `yaml:"paste_key"`         // paste keystroke for inject=paste (xdotool key spec); default Shift+Insert
	AutoPaste        bool    `yaml:"auto_paste"`        // inject=paste: auto-paste after dictation (false = copy to clipboard only)
	PostProcess      string  `yaml:"postprocess"`       // path to a post-processing script (stdin=transcript, stdout=improved); empty = off
	PostProcessOn    bool    `yaml:"postprocess_on"`    // run post-processing at startup (runtime-toggled in the tray); default false
	HistorySize      int     `yaml:"history_size"`      // dictations kept in the history / tray menu
	HistoryFile      string  `yaml:"history_file"`      // path to the dictation history (JSONL)
	DebugRecord      string  `yaml:"debug_record"`      // debug: dump raw recordings near this WAV path (empty = off)
	DebugRecordKeep  int     `yaml:"debug_record_keep"` // debug: how many recent raw recordings to retain (min 1)
}

// Defaults returns the default configuration (used when no file is present).
func Defaults() Config {
	home := os.Getenv("HOME")
	return Config{
		Hotkey:           Hotkey{Mods: "Super+Control", Key: "d", Lang: "ru", LangShift: "en"},
		SilenceThreshold: 0.005,
		VADThreshold:     0.3,
		Model:            filepath.Join(home, ".local/share/dictation/whisper.cpp/models/ggml-large-v3.bin"),
		VAD:              filepath.Join(home, ".local/share/dictation/whisper.cpp/models/ggml-silero-v5.1.2.bin"),
		Socket:           socketDefault(),
		Backend:          "auto",
		Inject:           "paste", // clipboard paste: instant, block insert, layout-independent
		AutoPaste:        true,
		HistorySize:      256,
		HistoryFile:      stateDefault(),
		DebugRecordKeep:  3,
	}
}

// Path is the path to the configuration file.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "vole", "config.yaml")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "vole", "config.yaml")
}

// Load reads the config: defaults ← file (if any) ← environment variables.
func Load() Config {
	c := Defaults()
	if data, err := os.ReadFile(Path()); err == nil {
		_ = yaml.Unmarshal(data, &c) // partial override on top of defaults
	}
	// env has the highest priority (compatibility and quick overrides)
	if v := os.Getenv("VOLE_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("VOLE_VAD"); v != "" {
		c.VAD = v
	}
	if v := os.Getenv("VOLE_SOCK"); v != "" {
		c.Socket = v
	}
	if v := os.Getenv("VOLE_BACKEND"); v != "" {
		c.Backend = v
	}
	if v := os.Getenv("VOLE_RECORD"); v != "" {
		c.DebugRecord = v
	}
	if v := os.Getenv("VOLE_POSTPROCESS"); v != "" {
		c.PostProcess = v
	}
	c.Model = expandHome(c.Model)
	c.VAD = expandHome(c.VAD)
	c.Socket = expandHome(c.Socket)
	c.HistoryFile = expandHome(c.HistoryFile)
	c.DebugRecord = expandHome(c.DebugRecord)
	c.PostProcess = expandHome(c.PostProcess)
	return c
}

func socketDefault() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "vole.sock")
	}
	return "/tmp/vole.sock"
}

// stateDefault is the default dictation-history path under XDG_STATE_HOME.
func stateDefault() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(state, "vole", "history.jsonl")
}

// expandHome expands a leading ~/ to the home directory.
func expandHome(p string) string {
	if p == "~" {
		return os.Getenv("HOME")
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(os.Getenv("HOME"), p[2:])
	}
	return p
}
