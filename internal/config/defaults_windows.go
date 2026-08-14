//go:build windows

package config

import (
	"os"
	"path/filepath"
)

const defaultPipe = `\\.\pipe\vole`

// Defaults returns the default configuration for Windows.
func Defaults() Config {
	c := baseDefaults()
	c.Hotkey = Hotkey{Mods: "Control+Alt", Key: "d", Lang: "ru", LangShift: "en"}
	c.PasteKey = "ctrl+v"
	c.Inject = "paste"
	c.AutoPaste = true
	c.Model = filepath.Join(localAppData(), "vole", "models", "ggml-large-v3.bin")
	c.VAD = filepath.Join(localAppData(), "vole", "models", "ggml-silero-v5.1.2.bin")
	c.Socket = defaultPipe
	c.HistoryFile = filepath.Join(localAppData(), "vole", "history.jsonl")
	return c
}

// Path is the path to the configuration file (%AppData%\vole\config.yaml).
func Path() string {
	return filepath.Join(roamingAppData(), "vole", "config.yaml")
}

func localAppData() string {
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return d
	}
	return filepath.Join(homeDir(), "AppData", "Local")
}

func roamingAppData() string {
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return d
	}
	return filepath.Join(homeDir(), "AppData", "Roaming")
}
