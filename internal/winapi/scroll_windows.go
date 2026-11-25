//go:build windows

package winapi

import (
	"fmt"
	"image"
	"unsafe"
)

var (
	procWindowFromPoint     = user32.NewProc("WindowFromPoint")
	procGetAncestor         = user32.NewProc("GetAncestor")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procSendInput           = user32.NewProc("SendInput")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetCursorPos        = user32.NewProc("SetCursorPos")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
)

const (
	wmMouseWheel    = 0x020A
	inputMouseType  = 0
	mouseEventWheel = 0x0800
	vkEscape        = 0x1B
	gaRoot          = 2
	WheelDelta      = 120
)

type winPoint struct {
	X, Y int32
}

// mouseInput and sendMouseInput match Win32 INPUT (40 bytes on amd64).
type mouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type sendMouseInput struct {
	typ uint32
	pad uint32
	mi  mouseInput
}

// ScreenGrabber caches a GDI compatible DC and top-down DIBSection for repeated
// zero-allocation captures of the same physical screen rectangle during scrolling capture.
type ScreenGrabber struct {
	rect      image.Rectangle
	w, h      int
	hdcScreen uintptr
	hdcMem    uintptr
	hBmp      uintptr
	oldObj    uintptr
	bits      unsafe.Pointer
	closed    bool
}

// NewScreenGrabber allocates reusable GDI resources for repeatedly capturing rect.
func NewScreenGrabber(rect image.Rectangle) (*ScreenGrabber, error) {
	rect = rect.Canon()
	w, h := rect.Dx(), rect.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("winapi: invalid grabber rect %v", rect)
	}

	hdcScreen, _, err := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, fmt.Errorf("winapi: GetDC failed: %w", err)
	}

	hdcMem, _, err := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		procReleaseDC.Call(0, hdcScreen)
		return nil, fmt.Errorf("winapi: CreateCompatibleDC failed: %w", err)
	}

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
		procDeleteDC.Call(hdcMem)
		procReleaseDC.Call(0, hdcScreen)
		return nil, fmt.Errorf("winapi: CreateDIBSection failed: %w", err)
	}

	oldObj, _, _ := procSelectObject.Call(hdcMem, hBmp)
	return &ScreenGrabber{
		rect:      rect,
		w:         w,
		h:         h,
		hdcScreen: hdcScreen,
		hdcMem:    hdcMem,
		hBmp:      hBmp,
		oldObj:    oldObj,
		bits:      bits,
	}, nil
}

// GrabInto captures the current desktop pixels into dst (reusing dst if its
// bounds match w×h) so steady-state polling allocates zero Go heap memory.
func (g *ScreenGrabber) GrabInto(dst *image.RGBA) (*image.RGBA, error) {
	if g == nil || g.closed {
		return nil, fmt.Errorf("winapi: ScreenGrabber is closed")
	}
	ret, _, err := procBitBlt.Call(
		g.hdcMem,
		0, 0,
		uintptr(g.w), uintptr(g.h),
		g.hdcScreen,
		uintptr(g.rect.Min.X), uintptr(g.rect.Min.Y),
		srccopy|captureBlt,
	)
	if ret == 0 {
		return nil, fmt.Errorf("winapi: BitBlt failed: %w", err)
	}

	if dst == nil || dst.Bounds().Dx() != g.w || dst.Bounds().Dy() != g.h || dst.Bounds().Min != (image.Point{}) {
		dst = image.NewRGBA(image.Rect(0, 0, g.w, g.h))
	}

	byteLen := g.w * g.h * 4
	src := unsafe.Slice((*byte)(g.bits), byteLen)
	pix := dst.Pix
	for i := 0; i < byteLen; i += 4 {
		pix[i+0] = src[i+2] // R
		pix[i+1] = src[i+1] // G
		pix[i+2] = src[i+0] // B
		pix[i+3] = 255      // A
	}
	return dst, nil
}

// Close releases all GDI handles held by the grabber.
func (g *ScreenGrabber) Close() {
	if g == nil || g.closed {
		return
	}
	g.closed = true
	if g.hdcMem != 0 && g.oldObj != 0 {
		procSelectObject.Call(g.hdcMem, g.oldObj)
	}
	if g.hBmp != 0 {
		procDeleteObject.Call(g.hBmp)
		g.hBmp = 0
	}
	if g.hdcMem != 0 {
		procDeleteDC.Call(g.hdcMem)
		g.hdcMem = 0
	}
	if g.hdcScreen != 0 {
		procReleaseDC.Call(0, g.hdcScreen)
		g.hdcScreen = 0
	}
	g.bits = nil
}

// ScrollWindowAt injects a vertical mouse-wheel scroll (negative wheelDelta scrolls down)
// into the desktop window at physical coordinates (screenPxX, screenPxY).
// When useSendInput is false, it tries cursor-free PostMessage(WM_MOUSEWHEEL) first;
// when true, it dispatches a hardware-level SendInput(MOUSEEVENTF_WHEEL).
func ScrollWindowAt(screenPxX, screenPxY int, wheelDelta int, useSendInput bool) bool {
	if !useSendInput {
		// On amd64 Windows, POINT (8 bytes) is passed by value packed in one uintptr.
		packedPt := uintptr(uint32(int32(screenPxX))) | (uintptr(uint32(int32(screenPxY))) << 32)
		hwnd, _, _ := procWindowFromPoint.Call(packedPt)
		if hwnd != 0 {
			selfPID, _, _ := procGetCurrentProcessId.Call()
			var pid uint32
			procGetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
			if uintptr(pid) != selfPID {
				wParam := uintptr(uint16(int16(wheelDelta))) << 16
				lParam := uintptr(uint16(int16(screenPxX))) | (uintptr(uint16(int16(screenPxY))) << 16)
				r, _, _ := procPostMessageW.Call(hwnd, wmMouseWheel, wParam, lParam)
				return r != 0
			}
		}
		return false
	}

	procSetCursorPos.Call(uintptr(int32(screenPxX)), uintptr(int32(screenPxY)))
	inp := sendMouseInput{
		typ: inputMouseType,
		mi: mouseInput{
			mouseData: uint32(int32(wheelDelta)),
			dwFlags:   mouseEventWheel,
		},
	}
	sent, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	return sent == 1
}

// ActivateWindowAt brings the top-level desktop window under physical point
// (screenPxX, screenPxY) to foreground focus (skipping Koma's own windows) so
// manual mouse-wheel and keyboard scrolling work immediately without clicking.
// Safe to call even while a Koma overlay is still covering the point: if
// WindowFromPoint hits Koma's own process, it falls back to Z-order EnumWindows.
func ActivateWindowAt(screenPxX, screenPxY int) bool {
	var target uintptr
	packedPt := uintptr(uint32(int32(screenPxX))) | (uintptr(uint32(int32(screenPxY))) << 32)
	hwnd, _, _ := procWindowFromPoint.Call(packedPt)
	if hwnd != 0 {
		selfPID, _, _ := procGetCurrentProcessId.Call()
		var pid uint32
		procGetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if uintptr(pid) != selfPID {
			target = hwnd
			if root, _, _ := procGetAncestor.Call(hwnd, gaRoot); root != 0 {
				target = root
			}
		}
	}
	if target == 0 {
		pt := image.Pt(screenPxX, screenPxY)
		for _, w := range ListVisibleWindows() {
			if pt.In(w.Bounds) {
				target = w.Handle
				break
			}
		}
	}
	if target == 0 {
		return false
	}
	r, _, _ := procSetForegroundWindow.Call(target)
	return r != 0
}

// GetCursorPosPx returns the current physical cursor position on the virtual desktop.
func GetCursorPosPx() (x, y int, ok bool) {
	var pt winPoint
	r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	if r == 0 {
		return 0, 0, false
	}
	return int(pt.X), int(pt.Y), true
}

// IsEscapePressed reports whether the physical Escape key is currently held down.
func IsEscapePressed() bool {
	r, _, _ := procGetAsyncKeyState.Call(vkEscape)
	return r&0x8000 != 0
}
