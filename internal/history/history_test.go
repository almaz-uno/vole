package history

import (
	"path/filepath"
	"testing"
)

func TestStoreAddCapPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := New(path, 3)
	s.Add("один", "ru")
	s.Add("два", "ru")
	s.Add("   ", "ru") // whitespace-only is ignored
	s.Add("три", "ru")
	s.Add("четыре", "ru") // exceeds the cap of 3

	items := s.Items()
	if len(items) != 3 {
		t.Fatalf("len=%d, want 3 (capped)", len(items))
	}
	for i, w := range []string{"четыре", "три", "два"} { // newest first
		if items[i].Text != w {
			t.Errorf("items[%d]=%q, want %q", i, items[i].Text, w)
		}
		if items[i].Lang != "ru" {
			t.Errorf("items[%d].Lang=%q, want ru", i, items[i].Lang)
		}
	}

	// reload from the persisted file
	if got := New(path, 3).Items(); len(got) != 3 || got[0].Text != "четыре" {
		t.Errorf("reloaded=%v, want newest 'четыре' len 3", got)
	}
}

func TestStoreOnChangePrimes(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "h.jsonl"), 5)
	s.Add("hi", "en")
	var got []Entry
	s.OnChange(func(e []Entry) { got = e })
	if len(got) != 1 || got[0].Text != "hi" {
		t.Errorf("OnChange prime=%v, want one entry 'hi'", got)
	}
}
