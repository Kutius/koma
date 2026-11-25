//go:build !windows

package winapi

import (
	"errors"
	"image"
)

type Monitor struct {
	Handle  uintptr
	Bounds  image.Rectangle
	Work    image.Rectangle
	DPI     int
	Scale   float64
	Primary bool
}

type MonitorCapture struct {
	Monitor Monitor
	Image   *image.RGBA
}

type WindowInfo struct {
	Handle uintptr
	Title  string
	Bounds image.Rectangle
}

func ListMonitors() []Monitor                       { return nil }
func VirtualDesktopBounds() image.Rectangle         { return image.Rectangle{} }
func CaptureRect(image.Rectangle) (*image.RGBA, error) { return nil, errors.New("unsupported platform") }
func CaptureAllMonitors() ([]MonitorCapture, error) { return nil, errors.New("unsupported platform") }
func CapturePrimaryMonitor() (*image.RGBA, Monitor, error) {
	return nil, Monitor{}, errors.New("unsupported platform")
}
func ListVisibleWindows() []WindowInfo { return nil }
func TrimWorkingSet()                  {}

const WheelDelta = 120

type ScreenGrabber struct{}

func NewScreenGrabber(image.Rectangle) (*ScreenGrabber, error) {
	return nil, errors.New("unsupported platform")
}
func (*ScreenGrabber) GrabInto(*image.RGBA) (*image.RGBA, error) {
	return nil, errors.New("unsupported platform")
}
func (*ScreenGrabber) Close()                                {}
func ScrollWindowAt(int, int, int, bool) bool                { return false }
func ActivateWindowAt(int, int) bool                         { return false }
func GetCursorPosPx() (int, int, bool)                       { return 0, 0, false }
func IsEscapePressed() bool                                  { return false }
