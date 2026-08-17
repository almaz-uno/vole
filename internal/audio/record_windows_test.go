//go:build windows

package audio

import (
	"testing"

	"github.com/go-ole/go-ole"
)

func TestScoreCaptureDevice(t *testing.T) {
	sonar := scoreCaptureDevice("SteelSeries Sonar - Microphone (SteelSeries Sonar Virtual Audio Device)", true)
	arctis := scoreCaptureDevice("Микрофон (SteelSeries Arctis 1 Wireless)", false)
	cable := scoreCaptureDevice("CABLE Output (VB-Audio Virtual Cable)", false)
	if !(arctis > sonar && arctis > cable) {
		t.Fatalf("hardware mic should win: arctis=%d sonar=%d cable=%d", arctis, sonar, cable)
	}
	if sonar >= 0 {
		t.Fatalf("default Sonar virtual mic should score negative, got %d", sonar)
	}
}

func TestCOMInitNeedsUninitForSFalse(t *testing.T) {
	needsUninit, handled := comInitNeedsUninit(ole.NewError(sFalse))
	if !handled {
		t.Fatal("S_FALSE should be handled as successful COM initialization")
	}
	if !needsUninit {
		t.Fatal("S_FALSE must be balanced with CoUninitialize")
	}
}

func TestCOMInitDoesNotUninitChangedMode(t *testing.T) {
	needsUninit, handled := comInitNeedsUninit(ole.NewError(rpcEChangedMode))
	if !handled {
		t.Fatal("RPC_E_CHANGED_MODE should be handled")
	}
	if needsUninit {
		t.Fatal("RPC_E_CHANGED_MODE must not call CoUninitialize")
	}
}
