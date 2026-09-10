package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := homeDir()
	if home == "" {
		t.Fatal("homeDir is empty")
	}
	if got := expandHome("~"); got != home {
		t.Errorf("expandHome(~) = %q, want %q", got, home)
	}
	got := expandHome("~/vole")
	if !strings.HasPrefix(got, home) || filepath.Base(got) != "vole" {
		t.Errorf("expandHome(~/vole) = %q, want under %q", got, home)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute path rewritten: %q", got)
	}
}

func TestDefaults_Languages(t *testing.T) {
	c := Defaults()
	if c.Hotkey.Lang != "ru" || c.Hotkey.LangShift != "en" {
		t.Errorf("langs = %q/%q, want ru/en", c.Hotkey.Lang, c.Hotkey.LangShift)
	}
	if c.Hotkey.Key != "d" {
		t.Errorf("key = %q, want d", c.Hotkey.Key)
	}
	if !c.AutoPaste {
		t.Error("auto_paste default should be true")
	}
}

func TestDefaults_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows defaults")
	}
	c := Defaults()
	if c.Hotkey.Mods != "Control+Alt" {
		t.Errorf("mods = %q, want Control+Alt", c.Hotkey.Mods)
	}
	if c.PasteKey != "unicode" {
		t.Errorf("paste_key = %q, want unicode", c.PasteKey)
	}
	if c.Socket != `\\.\pipe\vole` {
		t.Errorf("socket = %q, want named pipe", c.Socket)
	}
	if !strings.Contains(filepath.ToSlash(c.Model), "vole/models/") {
		t.Errorf("model path %q should be under vole/models", c.Model)
	}
	if !strings.HasSuffix(c.HistoryFile, "history.jsonl") {
		t.Errorf("history_file = %q", c.HistoryFile)
	}
	p := Path()
	if !strings.Contains(strings.ToLower(p), "vole") || !strings.HasSuffix(p, "config.yaml") {
		t.Errorf("Path() = %q, want %%AppData%%\\vole\\config.yaml", p)
	}
}

func TestDefaults_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux defaults")
	}
	c := Defaults()
	if c.Hotkey.Mods != "Super+Control" {
		t.Errorf("mods = %q, want Super+Control", c.Hotkey.Mods)
	}
	if c.Socket == `\\.\pipe\vole` {
		t.Error("linux socket should not be a Windows named pipe")
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("VOLE_MODEL", filepath.Join(t.TempDir(), "model.bin"))
	t.Setenv("VOLE_SOCK", "custom-sock")
	t.Setenv("VOLE_BACKEND", "windows")
	c := Load()
	if !strings.HasSuffix(c.Model, "model.bin") {
		t.Errorf("VOLE_MODEL not applied: %q", c.Model)
	}
	if c.Socket != "custom-sock" {
		t.Errorf("VOLE_SOCK not applied: %q", c.Socket)
	}
	if c.Backend != "windows" {
		t.Errorf("VOLE_BACKEND not applied: %q", c.Backend)
	}
	_ = os.Stderr
}
