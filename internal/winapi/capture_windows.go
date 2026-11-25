//go:build windows

package winapi

import (
	"fmt"
	"image"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors    = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	procGetDpiForMonitor       = shcore.NewProc("GetDpiForMonitor")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procEnumWindows            = user32.NewProc("EnumWindows")
	procIsWindowVisible        = user32.NewProc("IsWindowVisible")
	procIsIconic               = user32.NewProc("IsIconic")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procGetWindowTextW         = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW   = user32.NewProc("GetWindowTextLengthW")
	procGetWindowThreadProcess = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowLongPtrW      = user32.NewProc("GetWindowLongPtrW")
	procGetCurrentProcessId    = kernel32.NewProc("GetCurrentProcessId")
	procDwmGetWindowAttribute  = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	srccopy        = 0x00CC0020
	captureBlt     = 0x40000000
	dibRGBColors   = 0
	biRGB          = 0
	mdtEffective   = 0
	monitorPrimary = 1

	dwmwaExtendedFrameBounds = 9
	dwmwaCloaked             = 14

	gwlExStyle       = -20
	wsExToolWindow   = 0x00000080
	wsExNoActivate   = 0x08000000
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

type monitorInfoEx struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
	SzDevice  [32]uint16
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
	BmiColors [1]uint32
}

// Monitor describes a physical display on Windows.
type Monitor struct {
	Handle  uintptr
	Bounds  image.Rectangle // physical pixels in virtual desktop coordinates
	Work    image.Rectangle // physical pixels
	DPI     int
	Scale   float64
	Primary bool
}

// MonitorCapture holds the frozen full-resolution capture of a single monitor.
type MonitorCapture struct {
	Monitor Monitor
	Image   *image.RGBA // 0,0 .. W,H in physical pixels
}

// ListMonitors enumerates all active monitors with physical pixel rects and DPI.
func ListMonitors() []Monitor {
	var mons []Monitor
	cb := syscall.NewCallback(func(hMon, hdc uintptr, lprc *winRect, lParam uintptr) uintptr {
		var mi monitorInfoEx
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		ret, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi)))
		if ret == 0 {
			return 1
		}
		dpi := 96
		var dpiX, dpiY uint32
		if r, _, _ := procGetDpiForMonitor.Call(hMon, mdtEffective, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY))); r == 0 && dpiX > 0 {
			dpi = int(dpiX)
		}
		mons = append(mons, Monitor{
			Handle:  hMon,
			Bounds:  image.Rect(int(mi.RcMonitor.Left), int(mi.RcMonitor.Top), int(mi.RcMonitor.Right), int(mi.RcMonitor.Bottom)),
			Work:    image.Rect(int(mi.RcWork.Left), int(mi.RcWork.Top), int(mi.RcWork.Right), int(mi.RcWork.Bottom)),
			DPI:     dpi,
			Scale:   float64(dpi) / 96.0,
			Primary: (mi.DwFlags & monitorPrimary) != 0,
		})
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	return mons
}

// VirtualDesktopBounds returns the bounding rectangle of the entire virtual screen in physical pixels.
func VirtualDesktopBounds() image.Rectangle {
	x, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	y, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	w, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	ix, iy := int(int32(x)), int(int32(y))
	iw, ih := int(int32(w)), int(int32(h))
	return image.Rect(ix, iy, ix+iw, iy+ih)
}

// CaptureRect captures a physical-pixel rectangle from the live Windows desktop into an *image.RGBA.
func CaptureRect(rect image.Rectangle) (*image.RGBA, error) {
	rect = rect.Canon()
	w, h := rect.Dx(), rect.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("winapi: invalid capture rect %v", rect)
	}

	hdcScreen, _, err := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, fmt.Errorf("winapi: GetDC failed: %w", err)
	}
	defer procReleaseDC.Call(0, hdcScreen)

	hdcMem, _, err := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return nil, fmt.Errorf("winapi: CreateCompatibleDC failed: %w", err)
	}
	defer procDeleteDC.Call(hdcMem)

	var bi bitmapInfo
	bi.BmiHeader = bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bi.BmiHeader)),
		BiWidth:       int32(w),
		BiHeight:      -int32(h), // top-down DIB
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: biRGB,
	}

	var bits unsafe.Pointer
	hBmp, _, err := procCreateDIBSection.Call(
		hdcMem,
		uintptr(unsafe.Pointer(&bi)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if hBmp == 0 || bits == nil {
		return nil, fmt.Errorf("winapi: CreateDIBSection failed: %w", err)
	}
	defer procDeleteObject.Call(hBmp)

	oldObj, _, _ := procSelectObject.Call(hdcMem, hBmp)
	defer procSelectObject.Call(hdcMem, oldObj)

	ret, _, err := procBitBlt.Call(
		hdcMem,
		0, 0,
		uintptr(w), uintptr(h),
		hdcScreen,
		uintptr(rect.Min.X), uintptr(rect.Min.Y),
		srccopy|captureBlt,
	)
	if ret == 0 {
		return nil, fmt.Errorf("winapi: BitBlt failed: %w", err)
	}

	byteLen := w * h * 4
	src := unsafe.Slice((*byte)(bits), byteLen)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	pix := dst.Pix

	// Convert BGRA (DIB) -> RGBA with opaque alpha in a tight loop.
	for i := 0; i < byteLen; i += 4 {
		pix[i+0] = src[i+2] // R
		pix[i+1] = src[i+1] // G
		pix[i+2] = src[i+0] // B
		pix[i+3] = 255      // A
	}
	return dst, nil
}

// CaptureAllMonitors captures every active monitor in one pass.
func CaptureAllMonitors() ([]MonitorCapture, error) {
	mons := ListMonitors()
	if len(mons) == 0 {
		b := VirtualDesktopBounds()
		img, err := CaptureRect(b)
		if err != nil {
			return nil, err
		}
		return []MonitorCapture{{
			Monitor: Monitor{Bounds: b, Work: b, DPI: 96, Scale: 1, Primary: true},
			Image:   img,
		}}, nil
	}
	out := make([]MonitorCapture, 0, len(mons))
	for _, m := range mons {
		img, err := CaptureRect(m.Bounds)
		if err != nil {
			return nil, err
		}
		out = append(out, MonitorCapture{Monitor: m, Image: img})
	}
	return out, nil
}

// CapturePrimaryMonitor captures the primary display at full physical resolution.
func CapturePrimaryMonitor() (*image.RGBA, Monitor, error) {
	mons := ListMonitors()
	for _, m := range mons {
		if m.Primary {
			img, err := CaptureRect(m.Bounds)
			return img, m, err
		}
	}
	b := VirtualDesktopBounds()
	img, err := CaptureRect(b)
	return img, Monitor{Bounds: b, Work: b, DPI: 96, Scale: 1, Primary: true}, err
}
