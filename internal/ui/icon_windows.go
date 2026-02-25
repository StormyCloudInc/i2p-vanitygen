//go:build windows

package ui

import (
	"syscall"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	pSendMessageW   = user32.NewProc("SendMessageW")
	pLoadImageW     = user32.NewProc("LoadImageW")
	pFindWindowW    = user32.NewProc("FindWindowW")
	pGetModuleHandleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

const (
	wmSetIcon   = 0x0080
	iconSmall   = 0
	iconBig     = 1
	imageIcon   = 1
	lrDefaultSize = 0x0040
	lrShared      = 0x8000
)

// setWindowIcon loads the embedded icon resource and applies it to the window
// with the given title. Called from a goroutine after a short delay to ensure
// the window has been created.
func setWindowIcon(title string) {
	titlePtr, _ := syscall.UTF16PtrFromString(title)

	// Find our window by title
	hwnd, _, _ := pFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return
	}

	// Get the current module handle (our exe)
	hInst, _, _ := pGetModuleHandleW.Call(0)
	if hInst == 0 {
		return
	}

	// Load the big icon (32x32 or system-scaled) from the exe resource.
	// Resource name "APP" matches go-winres default RT_GROUP_ICON id.
	appPtr, _ := syscall.UTF16PtrFromString("APP")

	bigIcon, _, _ := pLoadImageW.Call(
		hInst,
		uintptr(unsafe.Pointer(appPtr)),
		imageIcon,
		0, 0, // default size
		lrDefaultSize|lrShared,
	)
	if bigIcon != 0 {
		pSendMessageW.Call(hwnd, wmSetIcon, iconBig, bigIcon)
		pSendMessageW.Call(hwnd, wmSetIcon, iconSmall, bigIcon)
	}
}
