//go:build windows

package winapi

import (
	"image"
	"strings"
	"syscall"
	"unsafe"
)

// WindowInfo describes a visible top-level desktop window in physical pixel
// coordinates (top-most first in z-order).
type WindowInfo struct {
	Handle uintptr
	Title  string
	Bounds image.Rectangle
}

// ListVisibleWindows returns all visible, uncloaked, non-minimized top-level
// windows belonging to other processes, ordered front-to-back for smart window
// snapping during capture.
func ListVisibleWindows() []WindowInfo {
	selfPID, _, _ := procGetCurrentProcessId.Call()
	var results []WindowInfo

	cb := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}
		iconic, _, _ := procIsIconic.Call(hwnd)
		if iconic != 0 {
			return 1
		}

		// Skip our own app's windows (editor, overlays, pins).
		var pid uint32
		procGetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if uintptr(pid) == selfPID {
			return 1
		}

		// Skip cloaked UWP/virtual-desktop windows.
		var cloaked uint32
		r, _, _ := procDwmGetWindowAttribute.Call(
			hwnd,
			dwmwaCloaked,
			uintptr(unsafe.Pointer(&cloaked)),
			unsafe.Sizeof(cloaked),
		)
		if r == 0 && cloaked != 0 {
			return 1
		}

		// Skip pure tool/overlay ghost windows without title unless large.
		exIdx := int32(gwlExStyle)
		exStyle, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(exIdx))
		if (exStyle&wsExToolWindow) != 0 && (exStyle&wsExNoActivate) != 0 {
			return 1
		}

		// Prefer DWMWA_EXTENDED_FRAME_BOUNDS so invisible drop-shadow borders aren't included!
		var wr winRect
		r2, _, _ := procDwmGetWindowAttribute.Call(
			hwnd,
			dwmwaExtendedFrameBounds,
			uintptr(unsafe.Pointer(&wr)),
			unsafe.Sizeof(wr),
		)
		if r2 != 0 {
			procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		}

		bounds := image.Rect(int(wr.Left), int(wr.Top), int(wr.Right), int(wr.Bottom))
		if bounds.Dx() < 24 || bounds.Dy() < 24 {
			return 1
		}

		title := windowTitle(hwnd)
		if title == "" || title == "Program Manager" || title == "Windows Input Experience" {
			return 1
		}

		results = append(results, WindowInfo{
			Handle: hwnd,
			Title:  title,
			Bounds: bounds,
		})
		return 1
	})

	procEnumWindows.Call(cb, 0)
	return results
}

func windowTitle(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 || n > 512 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return strings.TrimSpace(syscall.UTF16ToString(buf))
}
