// Package models downloads ggml whisper models from HuggingFace on demand.
package models

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	hfWhisper = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"
	hfVAD     = "https://huggingface.co/ggml-org/whisper-vad/resolve/main/"
)

// URLFor returns the HuggingFace download URL for a known ggml model filename.
// Whisper models live in ggerganov/whisper.cpp; VAD models in ggml-org/whisper-vad.
func URLFor(path string) (string, bool) {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "ggml-") || !strings.HasSuffix(name, ".bin") {
		return "", false
	}
	if strings.Contains(name, "silero") || strings.Contains(name, "vad") {
		return hfVAD + name, true
	}
	return hfWhisper + name, true
}

// Exists reports whether the file is present and non-empty.
func Exists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}

// Ensure downloads path from HuggingFace if it is missing. onProgress, if not
// nil, is called periodically with bytes done and total (total may be -1 if the
// server does not report Content-Length). The download is atomic (.part + rename).
func Ensure(path string, onProgress func(done, total int64)) error {
	if Exists(path) {
		return nil
	}
	url, ok := URLFor(path)
	if !ok {
		return fmt.Errorf("models: unknown model %q (no download URL)", filepath.Base(path))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("models: download %s: %s", url, resp.Status)
	}

	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, cb: onProgress}
	if _, err := io.Copy(f, pr); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

type progressReader struct {
	r          io.Reader
	total      int64
	done, last int64
	cb         func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	// throttle callbacks to roughly every 8 MiB (and once at EOF)
	if p.cb != nil && (p.done-p.last >= 8<<20 || err != nil) {
		p.last = p.done
		p.cb(p.done, p.total)
	}
	return n, err
}
