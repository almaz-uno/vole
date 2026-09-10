//go:build linux

package config

import (
	"os"
	"path/filepath"
)

// Defaults returns the default configuration for Linux.
func Defaults() Config {
	c := baseDefaults()
	home := homeDir()
	c.Hotkey = Hotkey{Mods: "Super+Control", Key: "d", Lang: "ru", LangShift: "en"}
	c.Model = filepath.Join(home, ".local/share/dictation/whisper.cpp/models/ggml-large-v3.bin")
	c.VAD = filepath.Join(home, ".local/share/dictation/whisper.cpp/models/ggml-silero-v5.1.2.bin")
	c.Socket = socketDefault()
	c.HistoryFile = stateDefault()
	return c
}

// Path is the path to the configuration file.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "vole", "config.yaml")
	}
	return filepath.Join(homeDir(), ".config", "vole", "config.yaml")
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
		state = filepath.Join(homeDir(), ".local", "state")
	}
	return filepath.Join(state, "vole", "history.jsonl")
}
