//go:build linux

package whisper

/*
#cgo pkg-config: whisper
#cgo LDFLAGS: -Wl,--disable-new-dtags
#include <whisper.h>
*/
import "C"
