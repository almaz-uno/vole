// Package whisper is a thin CGO wrapper around whisper.cpp (GPU via the Vulkan backend).
package whisper

/*
#cgo pkg-config: whisper
#cgo LDFLAGS: -Wl,--disable-new-dtags
#include <whisper.h>
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"
)

// Context is a whisper model loaded into memory (VRAM when useGPU is set).
type Context struct {
	ctx     *C.struct_whisper_context
	vadPath string // path to the Silero VAD model ("" disables VAD)
}

func cbool(b bool) C._Bool {
	if b {
		return C._Bool(true)
	}
	return C._Bool(false)
}

// New loads a ggml model. useGPU=true enables the Vulkan backend.
// vadPath is the path to the Silero VAD model ("" disables VAD).
func New(modelPath string, useGPU bool, vadPath string) (*Context, error) {
	cpath := C.CString(modelPath)
	defer C.free(unsafe.Pointer(cpath))

	cparams := C.whisper_context_default_params()
	cparams.use_gpu = cbool(useGPU)

	ctx := C.whisper_init_from_file_with_params(cpath, cparams)
	if ctx == nil {
		return nil, fmt.Errorf("whisper: failed to load model %q", modelPath)
	}
	return &Context{ctx: ctx, vadPath: vadPath}, nil
}

// Close frees the model and its VRAM.
func (c *Context) Close() {
	if c.ctx != nil {
		C.whisper_free(c.ctx)
		c.ctx = nil
	}
}

// Transcribe transcribes 16 kHz mono float32 PCM.
// lang="" or "auto" auto-detects the language.
func (c *Context) Transcribe(samples []float32, lang string, threads int) (string, error) {
	if len(samples) == 0 {
		return "", nil
	}

	// Greedy for PTT responsiveness (~0.5s). For better quality, switch to
	// C.WHISPER_SAMPLING_BEAM_SEARCH + params.beam_search.beam_size.
	params := C.whisper_full_default_params(C.WHISPER_SAMPLING_GREEDY)
	params.n_threads = C.int(threads)
	params.no_timestamps = cbool(true)
	params.print_progress = cbool(false)
	params.print_realtime = cbool(false)
	params.print_special = cbool(false)
	params.translate = cbool(false)
	params.no_context = cbool(true)   // single pass — no prior context needed
	params.suppress_nst = cbool(true) // suppress non-speech tokens (fewer hallucinations)

	if lang != "" && lang != "auto" {
		clang := C.CString(lang)
		defer C.free(unsafe.Pointer(clang))
		params.language = clang
	} else {
		params.detect_language = cbool(true)
	}

	// VAD: trim pauses before transcription — this avoids whisper looping
	// ("phrase phrase phrase") and hallucinations on silence.
	if c.vadPath != "" {
		cvad := C.CString(c.vadPath)
		defer C.free(unsafe.Pointer(cvad))
		params.vad = cbool(true)
		params.vad_model_path = cvad
		params.vad_params = C.whisper_vad_default_params()
	}

	ret := C.whisper_full(
		c.ctx, params,
		(*C.float)(unsafe.Pointer(&samples[0])), C.int(len(samples)),
	)
	if ret != 0 {
		return "", fmt.Errorf("whisper_full: code %d", int(ret))
	}

	n := int(C.whisper_full_n_segments(c.ctx))
	var sb strings.Builder
	for i := 0; i < n; i++ {
		seg := C.whisper_full_get_segment_text(c.ctx, C.int(i))
		sb.WriteString(C.GoString(seg))
	}
	return strings.TrimSpace(sb.String()), nil
}
