package deej

import (
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"go.uber.org/zap"
	"golang.org/x/sys/windows"
)

const (
	wmDeviceChange           uint32  = 0x0219
	dbtDeviceArrival         uintptr = 0x8000
	dbtDevtypDeviceInterface uint32  = 0x00000005
	deviceNotifyWindowHandle uint32  = 0x00000000
	hwndMessage              uintptr = ^uintptr(2) // (HWND)-3
)

// GUID_DEVINTERFACE_COMPORT — {86E0D1E0-8089-11D0-9CE4-08003E301F73}
var comPortGUID = windows.GUID{
	Data1: 0x86E0D1E0,
	Data2: 0x8089,
	Data3: 0x11D0,
	Data4: [8]byte{0x9C, 0xE4, 0x08, 0x00, 0x3E, 0x30, 0x1F, 0x73},
}

var (
	modUser32               = syscall.NewLazyDLL("user32.dll")
	procCreateWindowExW     = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW      = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow       = modUser32.NewProc("DestroyWindow")
	procDispatchMessageW    = modUser32.NewProc("DispatchMessageW")
	procGetMessageW         = modUser32.NewProc("GetMessageW")
	procPostMessageW        = modUser32.NewProc("PostMessageW")
	procRegisterClassExW    = modUser32.NewProc("RegisterClassExW")
	procRegisterDeviceNotif = modUser32.NewProc("RegisterDeviceNotificationW")
	procUnregisterDevNotif  = modUser32.NewProc("UnregisterDeviceNotification")
)

// wndClassEx mirrors WNDCLASSEXW. Field layout matches Win32 on amd64.
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

// windowsMsg mirrors MSG. Field layout matches Win32 on amd64.
// Go inserts 4 bytes of padding after Message to align WParam on an 8-byte boundary,
// matching the C struct layout.
type windowsMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
}

// devBroadcastDeviceInterface mirrors DEV_BROADCAST_DEVICEINTERFACE for the notification filter.
type devBroadcastDeviceInterface struct {
	Size       uint32
	DeviceType uint32
	Reserved   uint32
	ClassGuid  windows.GUID
	Name       [1]uint16
}

// hotplugArrivalCh is set by waitForSerialDevice and read by hotplugWndProc.
// Safe: waitForSerialDevice is single-instance.
var hotplugArrivalCh chan struct{}

func hotplugWndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	if uint32(message) == wmDeviceChange && wParam == dbtDeviceArrival {
		if ch := hotplugArrivalCh; ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

// waitForSerialDevice blocks until Windows fires a COM port device-arrival notification,
// then returns after a brief settling delay to allow the CDC driver to become openable.
// The caller should immediately retry serial.Start() after this returns.
func waitForSerialDevice(logger *zap.SugaredLogger) {
	// Message windows must be created and pumped on the same OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, _ := syscall.UTF16PtrFromString("deejxhotplug")
	wc := wndClassEx{
		WndProc:   syscall.NewCallback(hotplugWndProc),
		ClassName: className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	// Ignore error — ERROR_CLASS_ALREADY_EXISTS is fine on repeated calls.
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// HWND_MESSAGE = (HWND)-3: message-only window, never shown on screen.
	hwnd, _, err := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0,
		hwndMessage, 0, 0, 0,
	)
	if hwnd == 0 {
		logger.Warnw("Failed to create device notification window, retrying after delay", "error", err)
		time.Sleep(2 * time.Second)
		return
	}
	defer procDestroyWindow.Call(hwnd)

	filter := devBroadcastDeviceInterface{
		DeviceType: dbtDevtypDeviceInterface,
		ClassGuid:  comPortGUID,
	}
	filter.Size = uint32(unsafe.Sizeof(filter))

	hNotif, _, _ := procRegisterDeviceNotif.Call(
		hwnd,
		uintptr(unsafe.Pointer(&filter)),
		uintptr(deviceNotifyWindowHandle),
	)
	if hNotif != 0 {
		defer procUnregisterDevNotif.Call(hNotif)
	}

	arrivalCh := make(chan struct{}, 1)
	hotplugArrivalCh = arrivalCh
	defer func() { hotplugArrivalCh = nil }()

	logger.Debug("Waiting for COM port device arrival")

	// Post WM_QUIT to break the message loop when the arrival fires.
	go func() {
		<-arrivalCh
		procPostMessageW.Call(hwnd, 0x0012, 0, 0) // WM_QUIT = 0x0012
	}()

	var m windowsMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		// GetMessage returns 0 on WM_QUIT, -1 (as large uintptr) on error.
		if ret == 0 || ret == ^uintptr(0) {
			break
		}
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	// Give the CDC driver a moment to finish enumerating before the caller opens the port.
	time.Sleep(500 * time.Millisecond)
	logger.Debug("COM port arrival detected, retrying serial connection")
}
