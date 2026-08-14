//go:build windows

package whisper

/*
#cgo CFLAGS: -I${SRCDIR}/../../deps/whisper/include
#cgo LDFLAGS: -L${SRCDIR}/../../deps/whisper/lib -lwhisper -lggml -lggml-base -lggml-cpu -lggml-vulkan -lvulkan-1 -lstdc++ -lm
#include <whisper.h>
*/
import "C"
