package deej

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	wca "github.com/moutend/go-wca"
	"go.uber.org/zap"
	"golang.org/x/sys/windows"
)

// setupapi.dll and hid.dll — loaded lazily, no CGO required
var (
	modSetupAPI = syscall.NewLazyDLL("setupapi.dll")
	modHID      = syscall.NewLazyDLL("hid.dll")

	procSetupDiGetClassDevs             = modSetupAPI.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces     = modSetupAPI.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetail = modSetupAPI.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList    = modSetupAPI.NewProc("SetupDiDestroyDeviceInfoList")
	procHidDGetHidGuid                  = modHID.NewProc("HidD_GetHidGuid")
	procHidDGetPreparsedData            = modHID.NewProc("HidD_GetPreparsedData")
	procHidDFreePreparsedData           = modHID.NewProc("HidD_FreePreparsedData")
	procHidPGetCaps                     = modHID.NewProc("HidP_GetCaps")
)

const (
	digcfPresent         = 0x00000002
	digcfDeviceInterface = 0x00000010
	invalidHandleValue   = ^uintptr(0)

	hidpStatusSuccess = 0x00110000

	// micMuteUsagePage/micMuteUsage identify the RGB button's dedicated
	// top-level collection (firmware's kMicMuteDesc in main.cpp) - SERENITY's
	// composite HID interface also exposes a separate Consumer Control
	// collection (Play/Pause) on the same VID/PID, as its own top-level
	// collection (Windows splits each into its own device path, e.g.
	// HID\VID_xxxx&PID_xxxx&MI_02&COL01 vs &COL02). Matching on VID/PID alone
	// picks whichever collection enumerates first, which silently grabbed the
	// wrong one once a second collection existed - must check actual usage.
	micMuteUsagePage = 0xFF00
	micMuteUsage     = 0x01
)

// hidpCaps mirrors the fixed-size HIDP_CAPS struct (hidpi.h) - must be the
// exact real size (62 bytes) since HidP_GetCaps writes into it directly;
// only Usage/UsagePage are actually read here.
type hidpCaps struct {
	Usage                     uint16
	UsagePage                 uint16
	InputReportByteLength     uint16
	OutputReportByteLength    uint16
	FeatureReportByteLength   uint16
	Reserved                  [17]uint16
	NumberLinkCollectionNodes uint16
	NumberInputButtonCaps     uint16
	NumberInputValueCaps      uint16
	NumberInputDataIndices    uint16
	NumberOutputButtonCaps    uint16
	NumberOutputValueCaps     uint16
	NumberOutputDataIndices   uint16
	NumberFeatureButtonCaps   uint16
	NumberFeatureValueCaps    uint16
	NumberFeatureDataIndices  uint16
}

// matchesMicMuteCollection reports whether the opened HID handle is the RGB
// button's vendor-defined top-level collection (vs. e.g. the Consumer Control
// one sharing the same VID/PID).
func matchesMicMuteCollection(handle windows.Handle) bool {
	var preparsedData uintptr

	ret, _, _ := procHidDGetPreparsedData.Call(uintptr(handle), uintptr(unsafe.Pointer(&preparsedData)))
	if ret == 0 {
		return false
	}
	defer procHidDFreePreparsedData.Call(preparsedData)

	var caps hidpCaps
	status, _, _ := procHidPGetCaps.Call(preparsedData, uintptr(unsafe.Pointer(&caps)))

	return status == hidpStatusSuccess && caps.UsagePage == micMuteUsagePage && caps.Usage == micMuteUsage
}

type hidGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type spDeviceInterfaceData struct {
	CbSize             uint32
	InterfaceClassGuid hidGUID
	Flags              uint32
	Reserved           uintptr
}

// spDeviceInterfaceDetailHeader mirrors the fixed part of SP_DEVICE_INTERFACE_DETAIL_DATA_W.
// unsafe.Sizeof of this struct gives the correct cbSize value for SetupDiGetDeviceInterfaceDetailW.
type spDeviceInterfaceDetailHeader struct {
	CbSize     uint32
	DevicePath [1]uint16
}

// spDeviceInterfaceDetailData holds the header plus a large enough path buffer.
type spDeviceInterfaceDetailData struct {
	CbSize     uint32
	DevicePath [2048]uint16
}

func getHIDClassGUID() hidGUID {
	var guid hidGUID
	procHidDGetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	return guid
}

// openSERENITY enumerates HID devices and returns the SERENITY device as an io.ReadCloser.
func openSERENITY() (io.ReadCloser, error) {
	guid := getHIDClassGUID()

	hDevInfo, _, _ := procSetupDiGetClassDevs.Call(
		uintptr(unsafe.Pointer(&guid)),
		0,
		0,
		digcfPresent|digcfDeviceInterface,
	)
	if hDevInfo == invalidHandleValue {
		return nil, errors.New("SetupDiGetClassDevs returned invalid handle")
	}
	defer procSetupDiDestroyDeviceInfoList.Call(hDevInfo)

	vidStr := fmt.Sprintf("vid_%04x", hidVendorID)
	pidStr := fmt.Sprintf("pid_%04x", hidProductID)
	cbSize := uint32(unsafe.Sizeof(spDeviceInterfaceDetailHeader{}))

	for i := uint32(0); ; i++ {
		var ifaceData spDeviceInterfaceData
		ifaceData.CbSize = uint32(unsafe.Sizeof(ifaceData))

		ret, _, _ := procSetupDiEnumDeviceInterfaces.Call(
			hDevInfo,
			0,
			uintptr(unsafe.Pointer(&guid)),
			uintptr(i),
			uintptr(unsafe.Pointer(&ifaceData)),
		)
		if ret == 0 {
			break
		}

		var detail spDeviceInterfaceDetailData
		detail.CbSize = cbSize

		procSetupDiGetDeviceInterfaceDetail.Call(
			hDevInfo,
			uintptr(unsafe.Pointer(&ifaceData)),
			uintptr(unsafe.Pointer(&detail)),
			uintptr(unsafe.Sizeof(detail)),
			0,
			0,
		)

		path := syscall.UTF16ToString(detail.DevicePath[:])
		lower := strings.ToLower(path)

		if strings.Contains(lower, vidStr) && strings.Contains(lower, pidStr) {
			pathPtr, err := syscall.UTF16PtrFromString(path)
			if err != nil {
				return nil, fmt.Errorf("convert device path: %w", err)
			}

			handle, err := windows.CreateFile(
				pathPtr,
				windows.GENERIC_READ,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
				nil,
				windows.OPEN_EXISTING,
				0,
				0,
			)
			if err != nil {
				// this VID/PID can have multiple top-level collections (e.g. the
				// Consumer Control one) - a share violation on one candidate
				// shouldn't abort the search for the right one.
				continue
			}

			if !matchesMicMuteCollection(handle) {
				windows.CloseHandle(handle)
				continue
			}

			return &winHIDHandle{handle: handle}, nil
		}
	}

	return nil, errors.New("SERENITY HID device not found")
}

type winHIDHandle struct {
	handle windows.Handle
}

func (h *winHIDHandle) Read(p []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(h.handle, p, &n, nil)
	return int(n), err
}

func (h *winHIDHandle) Close() error {
	return windows.CloseHandle(h.handle)
}

// windowsMicMuter implements MicMuter via WASAPI/MMDeviceAPI.
type windowsMicMuter struct {
	logger *zap.SugaredLogger
}

func newMicMuter(logger *zap.SugaredLogger) (MicMuter, error) {
	return &windowsMicMuter{logger: logger.Named("mic_muter")}, nil
}

// withCaptureVolume initializes COM and the default capture endpoint's
// IAudioEndpointVolume, then hands it to fn for the duration of the call.
// Pins this goroutine to its current OS thread for the duration of the call -
// these are hand-rolled syscall-based COM bindings (go-wca), and letting the Go
// scheduler migrate this goroutine to a different OS thread mid-call-chain (it's
// never otherwise pinned - this runs on HIDManager's read-loop goroutine) was
// observed to corrupt the Go heap (runtime "fatal error: fault", not a normal
// panic) rather than cleanly erroring. Unlocked again before returning since
// nothing here needs to outlive the call (contrast with the master-volume
// watcher's registration in session_finder_windows.go, which is deliberately
// never unlocked because it must persist).
func (m *windowsMicMuter) withCaptureVolume(fn func(aev *wca.IAudioEndpointVolume) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		const eFalse = 1
		oleError := &ole.OleError{}
		if errors.As(err, &oleError) {
			if oleError.Code() != eFalse {
				return fmt.Errorf("CoInitializeEx: %w", err)
			}
		} else {
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	}
	defer ole.CoUninitialize()

	var de *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(
		wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL,
		wca.IID_IMMDeviceEnumerator, &de,
	); err != nil {
		return fmt.Errorf("create IMMDeviceEnumerator: %w", err)
	}
	defer de.Release()

	var dd *wca.IMMDevice
	if err := de.GetDefaultAudioEndpoint(wca.ECapture, wca.EConsole, &dd); err != nil {
		return fmt.Errorf("get default capture endpoint: %w", err)
	}
	defer dd.Release()

	var aev *wca.IAudioEndpointVolume
	if err := dd.Activate(wca.IID_IAudioEndpointVolume, wca.CLSCTX_ALL, nil, &aev); err != nil {
		return fmt.Errorf("activate IAudioEndpointVolume: %w", err)
	}
	defer aev.Release()

	return fn(aev)
}

func (m *windowsMicMuter) ToggleMute() error {
	// Deliberately doesn't touch m (the receiver) from inside the closure -
	// only aev and a plain local. The crash reproduced here consistently
	// pointed at m.logger.Debugw called from inside this closure immediately
	// after the COM calls returned; IsMuted's closure (which never touches m)
	// never crashed. Logging after withCaptureVolume returns instead, fully
	// outside the COM/syscall call chain, avoids whatever that interaction is.
	var nowMuted bool
	err := m.withCaptureVolume(func(aev *wca.IAudioEndpointVolume) error {
		var muted bool
		if err := aev.GetMute(&muted); err != nil {
			return fmt.Errorf("get mute state: %w", err)
		}

		if err := aev.SetMute(!muted, nil); err != nil {
			return fmt.Errorf("set mute state: %w", err)
		}

		nowMuted = !muted
		return nil
	})
	if err != nil {
		return err
	}

	m.logger.Debugw("Toggled mic mute", "nowMuted", nowMuted)
	return nil
}

// IsMuted reports the current system microphone mute state.
func (m *windowsMicMuter) IsMuted() (bool, error) {
	var muted bool
	err := m.withCaptureVolume(func(aev *wca.IAudioEndpointVolume) error {
		return aev.GetMute(&muted)
	})
	if err != nil {
		return false, fmt.Errorf("get mute state: %w", err)
	}
	return muted, nil
}
