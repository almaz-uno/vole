//go:build windows

package audio

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

const (
	audclntStreamflagsAutoconvertPCM    = 0x80000000
	audclntStreamflagsSRCDefaultQuality = 0x08000000
	audclntBufferflagsSilent            = 0x2
)

// Start begins WASAPI shared-mode capture at 16 kHz s16le mono. Windows
// resamples via AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM.
func (r *Recorder) Start() error {
	r.reset()
	errCh := make(chan error, 1)
	go r.captureLoop(errCh)
	select {
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Second):
		r.requestStop()
		return fmt.Errorf("wasapi: capture did not start")
	}
}

// Stop stops capture and returns the accumulated audio as float32.
func (r *Recorder) Stop() []float32 {
	done := r.requestStop()
	if done != nil {
		<-done
	}
	return r.snapshot()
}

func (r *Recorder) captureLoop(started chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(r.done)

	uninit, err := comInit()
	if err != nil {
		started <- fmt.Errorf("wasapi: CoInitializeEx: %w", err)
		return
	}
	defer uninit()

	var mmde *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(
		wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL,
		wca.IID_IMMDeviceEnumerator, &mmde,
	); err != nil {
		started <- fmt.Errorf("wasapi: device enumerator: %w", err)
		return
	}
	defer mmde.Release()

	mmd, name, err := openCaptureDevice(mmde)
	if err != nil {
		started <- err
		return
	}
	defer mmd.Release()
	fmt.Fprintf(os.Stderr, "[vole] wasapi: capture %q\n", name)

	var ac *wca.IAudioClient
	if err := mmd.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &ac); err != nil {
		started <- fmt.Errorf("wasapi: activate IAudioClient: %w", err)
		return
	}
	defer ac.Release()

	wfx := &wca.WAVEFORMATEX{
		WFormatTag:      wca.WAVE_FORMAT_PCM,
		NChannels:       1,
		NSamplesPerSec:  SampleRate,
		WBitsPerSample:  16,
		NBlockAlign:     2,
		NAvgBytesPerSec: SampleRate * 2,
		CbSize:          0,
	}
	const flags = audclntStreamflagsAutoconvertPCM | audclntStreamflagsSRCDefaultQuality
	// 100 ms buffer.
	const bufferHNS = 100 * 10000
	if err := ac.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, flags, bufferHNS, 0, wfx, nil); err != nil {
		started <- fmt.Errorf("wasapi: initialize 16 kHz PCM: %w", err)
		return
	}

	var acc *wca.IAudioCaptureClient
	if err := ac.GetService(wca.IID_IAudioCaptureClient, &acc); err != nil {
		started <- fmt.Errorf("wasapi: IAudioCaptureClient: %w", err)
		return
	}
	defer acc.Release()

	if err := ac.Start(); err != nil {
		started <- fmt.Errorf("wasapi: start: %w", err)
		return
	}
	defer ac.Stop()

	// Snapshot the stop channel before handing control back: Stop can run the
	// moment started is received.
	stop := r.stopChan()
	started <- nil

	for {
		select {
		case <-stop:
			return
		default:
		}

		var packet uint32
		if err := acc.GetNextPacketSize(&packet); err != nil {
			return
		}
		if packet == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		var data *byte
		var nFrames, flags uint32
		if err := acc.GetBuffer(&data, &nFrames, &flags, nil, nil); err != nil {
			return
		}
		nbytes := int(nFrames) * int(wfx.NBlockAlign)
		if nbytes > 0 && flags&audclntBufferflagsSilent == 0 && data != nil {
			chunk := unsafe.Slice(data, nbytes)
			cp := make([]byte, nbytes)
			copy(cp, chunk)
			r.appendPCM(cp)
		} else if nbytes > 0 {
			r.appendPCM(make([]byte, nbytes)) // silence
		}
		_ = acc.ReleaseBuffer(nFrames)
	}
}

// openCaptureDevice picks an active capture endpoint. Windows' default is often
// a virtual mixer (SteelSeries Sonar, VB-Cable) that is silent unless the app
// is registered with that mixer; other apps keep using it. vole skips those
// and prefers a hardware microphone so dictation works without changing the
// system default device.
func openCaptureDevice(mmde *wca.IMMDeviceEnumerator) (*wca.IMMDevice, string, error) {
	var def *wca.IMMDevice
	_ = mmde.GetDefaultAudioEndpoint(wca.ECapture, wca.EConsole, &def)
	var defID string
	if def != nil {
		_ = def.GetId(&defID)
	}

	var col *wca.IMMDeviceCollection
	if err := mmde.EnumAudioEndpoints(wca.ECapture, wca.DEVICE_STATE_ACTIVE, &col); err != nil {
		if def != nil {
			return def, captureDeviceName(def), nil
		}
		return nil, "", fmt.Errorf("wasapi: no capture device: %w", err)
	}
	defer col.Release()

	var n uint32
	if err := col.GetCount(&n); err != nil || n == 0 {
		if def != nil {
			return def, captureDeviceName(def), nil
		}
		return nil, "", fmt.Errorf("wasapi: no capture device")
	}

	bestScore := -1 << 30
	var best *wca.IMMDevice
	var bestName string
	for i := uint32(0); i < n; i++ {
		var mmd *wca.IMMDevice
		if err := col.Item(i, &mmd); err != nil {
			continue
		}
		name := captureDeviceName(mmd)
		var id string
		_ = mmd.GetId(&id)
		s := scoreCaptureDevice(name, id != "" && id == defID)
		if best == nil || s > bestScore {
			if best != nil {
				best.Release()
			}
			best, bestScore, bestName = mmd, s, name
			continue
		}
		mmd.Release()
	}
	if def != nil {
		def.Release()
	}
	if best == nil {
		return nil, "", fmt.Errorf("wasapi: no capture device")
	}
	return best, bestName, nil
}

func captureDeviceName(mmd *wca.IMMDevice) string {
	var ps *wca.IPropertyStore
	if err := mmd.OpenPropertyStore(wca.STGM_READ, &ps); err != nil {
		return ""
	}
	defer ps.Release()
	var pv wca.PROPVARIANT
	if err := ps.GetValue(&wca.PKEY_Device_FriendlyName, &pv); err != nil {
		return ""
	}
	return pv.String()
}

func scoreCaptureDevice(name string, isDefault bool) int {
	n := strings.ToLower(name)
	score := 0
	if isDefault {
		score += 10
	}
	for _, v := range []string{
		"sonar", "vb-audio", "cable", "voicemeeter",
		"stereo mix", "what u hear", "virtual audio",
	} {
		if strings.Contains(n, v) {
			score -= 100
		}
	}
	if strings.Contains(n, "microphone") || strings.Contains(n, "микрофон") {
		score += 20
	}
	return score
}

const (
	sFalse          = 1
	rpcEChangedMode = 0x80010106
)

// comInit initializes COM on the current OS thread. S_FALSE (already inited)
// and RPC_E_CHANGED_MODE (thread already STA) are not failures — go-ole
// reports S_FALSE as error 1 ("Incorrect function").
func comInit() (func(), error) {
	err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)
	needsUninit, handled := comInitNeedsUninit(err)
	if !handled {
		return nil, err
	}
	if needsUninit {
		return ole.CoUninitialize, nil
	}
	return func() {}, nil
}

func comInitNeedsUninit(err error) (needsUninit, handled bool) {
	if err == nil {
		return true, true
	}
	if oe, ok := err.(*ole.OleError); ok {
		switch oe.Code() {
		case sFalse:
			return true, true
		case rpcEChangedMode:
			return false, true
		}
	}
	return false, false
}
