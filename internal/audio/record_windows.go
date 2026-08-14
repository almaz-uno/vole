//go:build windows

package audio

import (
	"fmt"
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
	defer close(r.done)

	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		started <- fmt.Errorf("wasapi: CoInitializeEx: %w", err)
		return
	}
	defer ole.CoUninitialize()

	var mmde *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(
		wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL,
		wca.IID_IMMDeviceEnumerator, &mmde,
	); err != nil {
		started <- fmt.Errorf("wasapi: device enumerator: %w", err)
		return
	}
	defer mmde.Release()

	var mmd *wca.IMMDevice
	if err := mmde.GetDefaultAudioEndpoint(wca.ECapture, wca.EConsole, &mmd); err != nil {
		started <- fmt.Errorf("wasapi: no capture device: %w", err)
		return
	}
	defer mmd.Release()

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

	started <- nil

	stop := r.stop
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
