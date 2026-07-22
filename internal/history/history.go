// Package history keeps a capped, persistent log of recent dictations and
// notifies a listener (the tray) whenever it changes.
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is one recorded dictation.
type Entry struct {
	Time int64  `json:"ts"`             // unix seconds
	Text string `json:"text"`           // the transcribed text
	Lang string `json:"lang,omitempty"` // the language id of the dictation (e.g. "ru", "en")
}

// Store is a capped ring of recent dictations (newest first), persisted as JSONL.
type Store struct {
	mu       sync.Mutex
	path     string
	max      int
	items    []Entry
	onChange func([]Entry)
}

// New loads the history from path (if present), keeping at most max entries.
func New(path string, max int) *Store {
	if max <= 0 {
		max = 256
	}
	s := &Store{path: path, max: max}
	s.load()
	return s
}

func (s *Store) load() {
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Text != "" {
			s.items = append(s.items, e)
		}
	}
	if len(s.items) > s.max {
		s.items = s.items[:s.max]
	}
}

// Add records text as the newest entry, caps the ring, persists, and notifies.
// Empty or whitespace-only text is ignored. lang is the dictation's language id
// (e.g. "ru", "en"), stored so whisper can be seeded with a same-language prompt.
func (s *Store) Add(text, lang string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.mu.Lock()
	s.items = append([]Entry{{Time: time.Now().Unix(), Text: text, Lang: lang}}, s.items...)
	if len(s.items) > s.max {
		s.items = s.items[:s.max]
	}
	snapshot := append([]Entry(nil), s.items...)
	onChange := s.onChange
	s.mu.Unlock()

	s.persist(snapshot)
	if onChange != nil {
		onChange(snapshot)
	}
}

func (s *Store) persist(items []Entry) {
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range items {
		_ = enc.Encode(e) // one JSON object per line (JSONL)
	}
	if w.Flush() != nil || f.Close() != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, s.path)
}

// Items returns a snapshot of the entries, newest first.
func (s *Store) Items() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.items...)
}

// OnChange registers a listener invoked with a snapshot whenever the store
// changes; it is primed once with the current state.
func (s *Store) OnChange(fn func([]Entry)) {
	s.mu.Lock()
	s.onChange = fn
	items := append([]Entry(nil), s.items...)
	s.mu.Unlock()
	if fn != nil {
		fn(items)
	}
}
