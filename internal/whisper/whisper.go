// Package whisper is a thin CGO wrapper around whisper.cpp (GPU via the Vulkan backend).
package whisper

/*
#include <whisper.h>
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"os"
	"strings"
	"unsafe"
)

// Context is a whisper model loaded into memory (VRAM when useGPU is set).
type Context struct {
	ctx          *C.struct_whisper_context
	vadPath      string  // path to the Silero VAD model ("" disables VAD)
	vadThreshold float64 // speech probability threshold; <=0 keeps the whisper default
}

func cbool(b bool) C._Bool {
	if b {
		return C._Bool(true)
	}
	return C._Bool(false)
}

// New loads a ggml model. useGPU=true enables the Vulkan backend. vadPath is the
// path to the Silero VAD model ("" disables VAD). vadThreshold is the speech
// probability threshold (<=0 keeps whisper's default of 0.5); lower values catch
// quieter/whispered speech.
func New(modelPath string, useGPU bool, vadPath string, vadThreshold float64) (*Context, error) {
	cpath := C.CString(modelPath)
	defer C.free(unsafe.Pointer(cpath))

	cparams := C.whisper_context_default_params()
	cparams.use_gpu = cbool(useGPU)

	ctx := C.whisper_init_from_file_with_params(cpath, cparams)
	if ctx == nil {
		return nil, fmt.Errorf("whisper: failed to load model %q (Vulkan GPU init failed? install a Vulkan driver and rebuild whisper.cpp with GGML_VULKAN=ON; look for ggml_vulkan: in the log)", modelPath)
	}
	info := C.GoString(C.whisper_print_system_info())
	fmt.Fprintf(os.Stderr, "[vole] whisper: %s\n", strings.TrimSpace(info))
	return &Context{ctx: ctx, vadPath: vadPath, vadThreshold: vadThreshold}, nil
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
// prompt, when non-empty, seeds the decoder (whisper_full_params.initial_prompt):
// short prior context (e.g. the last dictation) that biases recognition toward
// the active vocabulary/topic — this notably steadies short, otherwise-ambiguous
// phrases. It is not a chat prompt: keep it to a sentence or two in the same
// language, or pass "" to disable. It MUST match the output script: a Cyrillic
// prompt corrupts an English dictation into transliterated Cyrillic, so callers
// disable it (pass "") when the output language differs from the history.
func (c *Context) Transcribe(samples []float32, lang string, threads int, prompt string) (string, error) {
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
	params.translate = cbool(false)   // transcription task (whisper's translate task is unreliable on the turbo model — translation is done by the post-process LLM instead)
	params.no_context = cbool(true)   // single pass — no prior context needed
	params.suppress_nst = cbool(true) // suppress non-speech tokens (fewer hallucinations)

	if lang != "" && lang != "auto" {
		clang := C.CString(lang)
		defer C.free(unsafe.Pointer(clang))
		params.language = clang
	} else {
		params.detect_language = cbool(true)
	}

	// initial_prompt: keep the C string alive until whisper_full returns (defer
	// frees it on function exit, after the call). no_context above is unrelated
	// (it suppresses re-feeding prior decoded segments, not the initial seed).
	if prompt != "" {
		cprompt := C.CString(prompt)
		defer C.free(unsafe.Pointer(cprompt))
		params.initial_prompt = cprompt
	}

	// VAD: trim pauses before transcription — this avoids whisper looping
	// ("phrase phrase phrase") and hallucinations on silence.
	if c.vadPath != "" {
		cvad := C.CString(c.vadPath)
		defer C.free(unsafe.Pointer(cvad))
		params.vad = cbool(true)
		params.vad_model_path = cvad
		vp := C.whisper_vad_default_params()
		if c.vadThreshold > 0 {
			vp.threshold = C.float(c.vadThreshold) // lower = catches quieter speech
		}
		params.vad_params = vp
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
