// Package config holds vole's configuration: defaults, the config.yaml file,
// and per-setting environment overrides.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Hotkey describes the PTT binding with Shift-based language switching.
type Hotkey struct {
	Mods      string `yaml:"mods"`       // "Super+Control" (Linux) / "Control+Alt" (Windows; at least one required)
	Key       string `yaml:"key"`        // "d"
	Lang      string `yaml:"lang"`       // language without Shift
	LangShift string `yaml:"lang_shift"` // language while Shift is held / alt hotkey
}

// Config is the full daemon configuration.
type Config struct {
	Hotkey             Hotkey  `yaml:"hotkey"`
	SilenceThreshold   float64 `yaml:"silence_threshold"`
	VADThreshold       float64 `yaml:"vad_threshold"` // Silero speech probability [0,1]; lower = catches quieter/whispered speech
	Model              string  `yaml:"model"`
	VAD                string  `yaml:"vad"`
	Socket             string  `yaml:"socket"`
	Backend            string  `yaml:"backend"`             // input/output backend: auto|x11|wayland|windows
	Inject             string  `yaml:"inject"`              // injection method: auto|type|paste
	PasteKey           string  `yaml:"paste_key"`           // inject=paste input; Linux: xdotool spec (default Shift+Insert); Windows: unicode or SendInput combo (default unicode)
	AutoPaste          bool    `yaml:"auto_paste"`          // inject=paste: auto-paste after dictation (false = copy to clipboard only)
	PostProcess        string  `yaml:"postprocess"`         // path to a post-processing script (stdin=transcript, stdout=improved); empty = off
	PostProcessOn      bool    `yaml:"postprocess_on"`      // run post-processing at startup (runtime-toggled in the tray); default false
	PostProcessTimeout int     `yaml:"postprocess_timeout"` // seconds to let the post-process script run before it is killed (default 30)
	WhisperPrompt      bool    `yaml:"whisper_prompt"`      // seed whisper with the last dictation as initial_prompt (steadies short phrases); default true
	Translate          bool    `yaml:"translate"`           // the alt/shift (dictate-alt) language translates speech to English: whisper transcribes with the primary (dictate) language pinned and the post-process LLM translates the clean source transcript to English; only meaningful with lang_shift: en and a post-process script. Default false
	EnglishInput       bool    `yaml:"english_input"`       // tray toggle: makes the Shift/dictate-alt combo transcribe English speech → English text (with the repair post-process hook) instead of translating Russian→English. The base combo is unaffected (always the base language). Runtime-toggled in the tray; default false.
	Merge              bool    `yaml:"merge"`               // tray toggle: a dictation started while the previous one is still being processed continues it — the new raw transcript is appended to the pending one and the whole text is post-processed again (the in-flight run is abandoned), so a thought can be finished in several takes and lands as one insertion. Runtime-toggled in the tray; default true.
	HistorySize        int     `yaml:"history_size"`        // dictations kept in the history / tray menu
	HistoryFile        string  `yaml:"history_file"`        // path to the dictation history (JSONL)
	DebugRecord        string  `yaml:"debug_record"`        // debug: dump raw recordings near this WAV path (empty = off)
	DebugRecordKeep    int     `yaml:"debug_record_keep"`   // debug: how many recent raw recordings to retain (min 1)
}

// baseDefaults returns OS-independent field defaults. OS-specific Defaults()
// fills paths, hotkey, paste_key, and socket on top of this.
func baseDefaults() Config {
	return Config{
		SilenceThreshold:   0.005,
		VADThreshold:       0.3,
		Backend:            "auto",
		Inject:             "paste", // clipboard paste: instant, block insert, layout-independent
		AutoPaste:          true,
		PostProcessTimeout: 30,   // generous: cloud/vision post-processing may take several seconds
		WhisperPrompt:      true, // seed whisper with the last dictation — steadies short phrases
		Merge:              true, // continue a dictation that is still being processed instead of starting a new one
		HistorySize:        256,
		DebugRecordKeep:    3,
	}
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

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE")
}

// expandHome expands a leading ~/ or ~\ to the home directory.
func expandHome(p string) string {
	home := homeDir()
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}
