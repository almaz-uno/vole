//go:build windows

package overlay

import (
	"reflect"
	"testing"
	"time"
)

func TestOverlayStoresDIBAddressAsPointer(t *testing.T) {
	field, ok := reflect.TypeOf(Overlay{}).FieldByName("bits")
	if !ok {
		t.Fatal("Overlay.bits field is missing")
	}
	if field.Type.Kind() != reflect.UnsafePointer {
		t.Fatalf("Overlay.bits kind = %v, want unsafe.Pointer", field.Type.Kind())
	}
}

func TestOverlayNewShowClose(t *testing.T) {
	o, err := New()
	if err != nil {
		t.Fatal(err)
	}
	o.Show("ru")
	time.Sleep(80 * time.Millisecond)
	o.SetLevel(0.3)
	o.SetMode(ModeProcessing)
	time.Sleep(80 * time.Millisecond)
	o.Hide()
	o.Close()
}
