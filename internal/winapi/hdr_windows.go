//go:build windows

package winapi

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	dxgiDLL  = windows.NewLazySystemDLL("dxgi.dll")
	d3d11DLL = windows.NewLazySystemDLL("d3d11.dll")

	procGetDisplayConfigBufferSizes = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig          = user32.NewProc("QueryDisplayConfig")
	procDisplayConfigGetDeviceInfo  = user32.NewProc("DisplayConfigGetDeviceInfo")
	procSetThreadDpiAwarenessCtx    = user32.NewProc("SetThreadDpiAwarenessContext")

	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
	procD3D11CreateDevice  = d3d11DLL.NewProc("D3D11CreateDevice")
)

const (
	qdcOnlyActivePaths = 0x00000002

	dcDeviceInfoGetSourceName        = 1
	dcDeviceInfoGetAdvancedColorInfo = 9
	dcDeviceInfoGetSDRWhiteLevel     = 11
	dcDeviceInfoGetAdvancedColor2    = 15 // Windows 11 24H2+: distinguishes WCG (ACM) vs HDR

	// DISPLAYCONFIG_ADVANCED_COLOR_MODE (Windows 11 24H2+ wingdi.h):
	//   0 = SDR, 1 = WCG (Auto Color Management), 2 = HDR
	dcAdvancedColorModeHDR = 2

	d3dDriverTypeUnknown      = 0
	d3d11CreateDeviceBGRA     = 0x20
	d3d11SDKVersion           = 7
	d3d11UsageStaging         = 3
	d3d11CPUAccessRead        = 0x20000
	d3d11MapRead              = 1

	dxgiFormatR16G16B16A16Float = 10
	dxgiFormatB8G8R8A8Unorm     = 87

	dxgiErrorNotFound    = 0x887A0002
	dxgiErrorWaitTimeout = 0x887A0027

	// COM vtable slots (verified against Windows SDK headers and MyGo's d3d11 backend).
	vtFactoryEnumAdapters1   = 12
	vtAdapterEnumOutputs     = 7
	vtOutputGetDesc          = 7
	vtOutput5DuplicateOut1   = 26
	vtDuplAcquireNextFrame   = 8
	vtDuplReleaseFrame       = 14
	vtDeviceCreateTexture2D  = 5
	vtTex2DGetDesc           = 10
	vtCtxMap                 = 14
	vtCtxUnmap               = 15
	vtCtxCopyResource        = 47
)

var (
	iidIDXGIFactory1   = windows.GUID{Data1: 0x770AAE78, Data2: 0xF26F, Data3: 0x4DBA, Data4: [8]byte{0xA8, 0x29, 0x25, 0x3C, 0x83, 0xD1, 0xB3, 0x87}}
	iidIDXGIOutput5    = windows.GUID{Data1: 0x80A07424, Data2: 0xAB52, Data3: 0x42EB, Data4: [8]byte{0x83, 0x3C, 0x0C, 0x42, 0xFD, 0x28, 0x2D, 0x98}}
	iidID3D11Texture2D = windows.GUID{Data1: 0x6F15AAF2, Data2: 0xD208, Data3: 0x4E89, Data4: [8]byte{0x9A, 0xB4, 0x48, 0x95, 0x35, 0xD3, 0x4F, 0x9C}}
)

type luid struct {
	LowPart  uint32
	HighPart int32
}

type dcRational struct {
	Numerator   uint32
	Denominator uint32
}

type dcPathSourceInfo struct {
	AdapterID   luid
	ID          uint32
	ModeInfoIdx uint32
	StatusFlags uint32
}

type dcPathTargetInfo struct {
	AdapterID        luid
	ID               uint32
	ModeInfoIdx      uint32
	OutputTechnology uint32
	Rotation         uint32
	Scaling          uint32
	RefreshRate      dcRational
	ScanLineOrdering uint32
	TargetAvailable  int32
	StatusFlags      uint32
}

type dcPathInfo struct {
	SourceInfo dcPathSourceInfo
	TargetInfo dcPathTargetInfo
	Flags      uint32
}

type dcModeInfo struct {
	InfoType  uint32
	ID        uint32
	AdapterID luid
	Union     [48]byte
}

type dcDeviceInfoHeader struct {
	Type      int32
	Size      uint32
	AdapterID luid
	ID        uint32
}

type dcSourceDeviceName struct {
	Header            dcDeviceInfoHeader
	ViewGdiDeviceName [32]uint16
}

type dcGetAdvancedColorInfo struct {
	Header              dcDeviceInfoHeader
	Value               uint32
	ColorEncoding       uint32
	BitsPerColorChannel uint32
}

type dcGetAdvancedColorInfo2 struct {
	Header              dcDeviceInfoHeader
	Value               uint32
	ColorEncoding       uint32
	BitsPerColorChannel uint32
	ActiveColorMode     uint32
}

type dcSDRWhiteLevel struct {
	Header        dcDeviceInfoHeader
	SDRWhiteLevel uint32
}

type monitorHDRInfo struct {
	HDREnabled   bool
	SDRWhiteNits float32
}

// queryMonitorsHDRInfo queries Windows DisplayConfig for every active display path,
// returning a map keyed by normalized GDI device name (e.g. `\\.\DISPLAY1`).
func queryMonitorsHDRInfo() map[string]monitorHDRInfo {
	var numPaths, numModes uint32
	r, _, _ := procGetDisplayConfigBufferSizes.Call(
		qdcOnlyActivePaths,
		uintptr(unsafe.Pointer(&numPaths)),
		uintptr(unsafe.Pointer(&numModes)),
	)
	if r != 0 || numPaths == 0 {
		return nil
	}
	paths := make([]dcPathInfo, numPaths)
	modes := make([]dcModeInfo, max(numModes, 1))
	r, _, _ = procQueryDisplayConfig.Call(
		qdcOnlyActivePaths,
		uintptr(unsafe.Pointer(&numPaths)),
		uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&numModes)),
		uintptr(unsafe.Pointer(&modes[0])),
		0,
	)
	if r != 0 {
		return nil
	}

	out := make(map[string]monitorHDRInfo, numPaths)
	for i := uint32(0); i < numPaths; i++ {
		p := paths[i]

		var srcName dcSourceDeviceName
		srcName.Header = dcDeviceInfoHeader{
			Type:      dcDeviceInfoGetSourceName,
			Size:      uint32(unsafe.Sizeof(srcName)),
			AdapterID: p.SourceInfo.AdapterID,
			ID:        p.SourceInfo.ID,
		}
		if rr, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&srcName))); rr != 0 {
			continue
		}
		devName := strings.ToUpper(windows.UTF16ToString(srcName.ViewGdiDeviceName[:]))
		if devName == "" {
			continue
		}

		hdrOn := false
		// Prefer Windows 11 24H2+ GET_ADVANCED_COLOR_INFO_2 so SDR Auto Color Management (WCG)
		// is never misclassified as HDR; fall back to GET_ADVANCED_COLOR_INFO on Win10/older Win11.
		var adv2 dcGetAdvancedColorInfo2
		adv2.Header = dcDeviceInfoHeader{
			Type:      dcDeviceInfoGetAdvancedColor2,
			Size:      uint32(unsafe.Sizeof(adv2)),
			AdapterID: p.TargetInfo.AdapterID,
			ID:        p.TargetInfo.ID,
		}
		if rr, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&adv2))); rr == 0 {
			hdrOn = adv2.ActiveColorMode == dcAdvancedColorModeHDR
		} else {
			var adv dcGetAdvancedColorInfo
			adv.Header = dcDeviceInfoHeader{
				Type:      dcDeviceInfoGetAdvancedColorInfo,
				Size:      uint32(unsafe.Sizeof(adv)),
				AdapterID: p.TargetInfo.AdapterID,
				ID:        p.TargetInfo.ID,
			}
			if rr2, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&adv))); rr2 == 0 {
				hdrOn = (adv.Value & 0x2) != 0 // advancedColorEnabled bit
			}
		}

		sdrNits := float32(80.0)
		var sdr dcSDRWhiteLevel
		sdr.Header = dcDeviceInfoHeader{
			Type:      dcDeviceInfoGetSDRWhiteLevel,
			Size:      uint32(unsafe.Sizeof(sdr)),
			AdapterID: p.TargetInfo.AdapterID,
			ID:        p.TargetInfo.ID,
		}
		if rr, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&sdr))); rr == 0 && sdr.SDRWhiteLevel > 0 {
			sdrNits = effectiveSDRWhiteNits(float32(sdr.SDRWhiteLevel) / 1000.0 * 80.0)
		}

		out[devName] = monitorHDRInfo{
			HDREnabled:   hdrOn,
			SDRWhiteNits: sdrNits,
		}
	}
	return out
}

type dxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates winRect
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type dxgiOutDuplPointerPos struct {
	X, Y    int32
	Visible int32
}

type dxgiOutDuplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerPosition           dxgiOutDuplPointerPos
	TotalMetadataBufferSize   uint32
	PointerShapeBufferSize    uint32
}

type d3d11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type d3d11MappedSubresource struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

// dpiAwarenessContextPerMonitorAwareV2 = ((DPI_AWARENESS_CONTEXT)-4).
const dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3)

// captureMonitorHDRDXGI captures a single monitor via DXGI Desktop Duplication
// (IDXGIOutput5::DuplicateOutput1) requesting 16-bit floating-point scRGB
// (DXGI_FORMAT_R16G16B16A16_FLOAT). Works cleanly on hybrid iGPU+dGPU systems
// by creating the D3D11 device on the exact adapter owning mon.Handle.
func captureMonitorHDRDXGI(mon Monitor) (*HDRFrame, error) {
	hdr, _, err := captureMonitorDXGI(mon, []uint32{dxgiFormatR16G16B16A16Float, dxgiFormatB8G8R8A8Unorm})
	return hdr, err
}

// captureMonitorDXGI runs the full pure-Go DXGI Desktop Duplication pipeline on
// mon using the requested DXGI formats. When the desktop surface is
// DXGI_FORMAT_R16G16B16A16_FLOAT it returns an *HDRFrame; when it is
// DXGI_FORMAT_B8G8R8A8_UNORM it returns its dimensions and format for diagnostics/tests.
func captureMonitorDXGI(mon Monitor, formats []uint32) (*HDRFrame, uint32, error) {
	if mon.Handle == 0 && mon.DeviceName == "" {
		return nil, 0, fmt.Errorf("winapi: missing monitor handle/device name for DXGI capture")
	}
	if len(formats) == 0 {
		return nil, 0, fmt.Errorf("winapi: no DXGI formats requested")
	}

	// Pin this goroutine to an OS thread for deterministic DXGI/D3D11 COM calls,
	// and ensure Per-Monitor DPI v2 awareness on the calling thread (Microsoft docs:
	// IDXGIOutput5::DuplicateOutput1 returns DXGI_ERROR_UNSUPPORTED without it).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var prevDPI uintptr
	if procSetThreadDpiAwarenessCtx.Find() == nil {
		prevDPI, _, _ = procSetThreadDpiAwarenessCtx.Call(dpiAwarenessContextPerMonitorAwareV2)
		if prevDPI != 0 {
			defer procSetThreadDpiAwarenessCtx.Call(prevDPI)
		}
	}

	var factory *comObj
	hr, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&iidIDXGIFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if int32(uint32(hr)) < 0 || factory == nil {
		return nil, 0, fmt.Errorf("winapi: CreateDXGIFactory1 failed: 0x%08X", uint32(hr))
	}
	defer factory.release()

	targetDev := strings.ToUpper(mon.DeviceName)
	var matchedAdapter, matchedOutput *comObj

	for ai := uintptr(0); ; ai++ {
		var adapter *comObj
		r, _, _ := syscall.SyscallN(
			factory.vtblSlot(vtFactoryEnumAdapters1),
			uintptr(unsafe.Pointer(factory)),
			ai,
			uintptr(unsafe.Pointer(&adapter)),
		)
		if uint32(r) == dxgiErrorNotFound || int32(uint32(r)) < 0 || adapter == nil {
			break
		}
		for oi := uintptr(0); ; oi++ {
			var output *comObj
			ro, _, _ := syscall.SyscallN(
				adapter.vtblSlot(vtAdapterEnumOutputs),
				uintptr(unsafe.Pointer(adapter)),
				oi,
				uintptr(unsafe.Pointer(&output)),
			)
			if uint32(ro) == dxgiErrorNotFound || int32(uint32(ro)) < 0 || output == nil {
				break
			}
			var desc dxgiOutputDesc
			rd, _, _ := syscall.SyscallN(
				output.vtblSlot(vtOutputGetDesc),
				uintptr(unsafe.Pointer(output)),
				uintptr(unsafe.Pointer(&desc)),
			)
			if int32(uint32(rd)) >= 0 {
				devName := strings.ToUpper(windows.UTF16ToString(desc.DeviceName[:]))
				if (mon.Handle != 0 && desc.Monitor == mon.Handle) || (targetDev != "" && devName == targetDev) {
					matchedAdapter = adapter
					matchedOutput = output
					break
				}
			}
			output.release()
		}
		if matchedOutput != nil {
			break
		}
		adapter.release()
	}

	if matchedAdapter == nil || matchedOutput == nil {
		return nil, 0, fmt.Errorf("winapi: DXGI output not found for monitor %q", mon.DeviceName)
	}
	defer matchedAdapter.release()
	defer matchedOutput.release()

	out5, qhr := matchedOutput.queryInterface(&iidIDXGIOutput5)
	if int32(qhr) < 0 || out5 == nil {
		return nil, 0, fmt.Errorf("winapi: QueryInterface(IDXGIOutput5) failed: 0x%08X", qhr)
	}
	defer out5.release()

	var device, ctx *comObj
	hr, _, _ = procD3D11CreateDevice.Call(
		uintptr(unsafe.Pointer(matchedAdapter)),
		d3dDriverTypeUnknown,
		0,
		d3d11CreateDeviceBGRA,
		0,
		0,
		d3d11SDKVersion,
		uintptr(unsafe.Pointer(&device)),
		0,
		uintptr(unsafe.Pointer(&ctx)),
	)
	if int32(uint32(hr)) < 0 || device == nil || ctx == nil {
		if device != nil {
			device.release()
		}
		if ctx != nil {
			ctx.release()
		}
		return nil, 0, fmt.Errorf("winapi: D3D11CreateDevice failed: 0x%08X", uint32(hr))
	}
	defer ctx.release()
	defer device.release()

	var dupl *comObj
	rd, _, _ := syscall.SyscallN(
		out5.vtblSlot(vtOutput5DuplicateOut1),
		uintptr(unsafe.Pointer(out5)),
		uintptr(unsafe.Pointer(device)),
		0,
		uintptr(len(formats)),
		uintptr(unsafe.Pointer(&formats[0])),
		uintptr(unsafe.Pointer(&dupl)),
	)
	if int32(uint32(rd)) < 0 || dupl == nil {
		return nil, 0, fmt.Errorf("winapi: DuplicateOutput1 failed: 0x%08X", uint32(rd))
	}
	defer dupl.release()

	// Acquire the desktop surface. On a freshly created duplication, Windows may either
	// return the current desktop immediately on frame 0 (even with LastPresentTime==0 on
	// a static screen) or emit a blank initial frame followed by the real frame shortly after.
	// We capture into staging on the first S_OK frame; if it had LastPresentTime==0 and
	// AccumulatedFrames==0, we give DWM one brief chance to deliver a presented frame and
	// fall back to the already-copied frame 0 if the desktop is static (WAIT_TIMEOUT).
	var stagingTex *comObj
	var desc d3d11Texture2DDesc
	haveFrame := false

	copyAcquiredFrame := func(res *comObj) error {
		srcTex, thr := res.queryInterface(&iidID3D11Texture2D)
		if int32(thr) < 0 || srcTex == nil {
			return fmt.Errorf("winapi: QueryInterface(ID3D11Texture2D) failed: 0x%08X", thr)
		}
		defer srcTex.release()

		var curDesc d3d11Texture2DDesc
		syscall.SyscallN(
			srcTex.vtblSlot(vtTex2DGetDesc),
			uintptr(unsafe.Pointer(srcTex)),
			uintptr(unsafe.Pointer(&curDesc)),
		)
		if curDesc.Width == 0 || curDesc.Height == 0 {
			return fmt.Errorf("winapi: empty DXGI desktop surface (%dx%d)", curDesc.Width, curDesc.Height)
		}
		if stagingTex == nil || curDesc.Width != desc.Width || curDesc.Height != desc.Height || curDesc.Format != desc.Format {
			if stagingTex != nil {
				stagingTex.release()
				stagingTex = nil
			}
			stagingDesc := d3d11Texture2DDesc{
				Width:          curDesc.Width,
				Height:         curDesc.Height,
				MipLevels:      1,
				ArraySize:      1,
				Format:         curDesc.Format,
				SampleCount:    1,
				SampleQuality:  0,
				Usage:          d3d11UsageStaging,
				BindFlags:      0,
				CPUAccessFlags: d3d11CPUAccessRead,
				MiscFlags:      0,
			}
			var st *comObj
			rc, _, _ := syscall.SyscallN(
				device.vtblSlot(vtDeviceCreateTexture2D),
				uintptr(unsafe.Pointer(device)),
				uintptr(unsafe.Pointer(&stagingDesc)),
				0,
				uintptr(unsafe.Pointer(&st)),
			)
			if int32(uint32(rc)) < 0 || st == nil {
				return fmt.Errorf("winapi: CreateTexture2D(staging) failed: 0x%08X", uint32(rc))
			}
			stagingTex = st
			desc = curDesc
		}
		syscall.SyscallN(
			ctx.vtblSlot(vtCtxCopyResource),
			uintptr(unsafe.Pointer(ctx)),
			uintptr(unsafe.Pointer(stagingTex)),
			uintptr(unsafe.Pointer(srcTex)),
		)
		haveFrame = true
		return nil
	}
	defer func() {
		if stagingTex != nil {
			stagingTex.release()
		}
	}()

	for attempt := 0; attempt < 4; attempt++ {
		var frameInfo dxgiOutDuplFrameInfo
		var res *comObj
		timeoutMs := uintptr(25)
		if attempt == 0 {
			timeoutMs = 120
		}
		ra, _, _ := syscall.SyscallN(
			dupl.vtblSlot(vtDuplAcquireNextFrame),
			uintptr(unsafe.Pointer(dupl)),
			timeoutMs,
			uintptr(unsafe.Pointer(&frameInfo)),
			uintptr(unsafe.Pointer(&res)),
		)
		if uint32(ra) == dxgiErrorWaitTimeout {
			if haveFrame {
				// Static desktop: keep the valid frame already copied into stagingTex.
				break
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if int32(uint32(ra)) < 0 || res == nil {
			if haveFrame {
				break
			}
			return nil, 0, fmt.Errorf("winapi: AcquireNextFrame failed: 0x%08X", uint32(ra))
		}
		copyErr := copyAcquiredFrame(res)
		res.release()
		syscall.SyscallN(dupl.vtblSlot(vtDuplReleaseFrame), uintptr(unsafe.Pointer(dupl)))
		if copyErr != nil {
			return nil, desc.Format, copyErr
		}
		if frameInfo.LastPresentTime != 0 || frameInfo.AccumulatedFrames > 0 {
			break
		}
		// Frame 0 had LastPresentTime==0; try once more briefly in case DWM is about to
		// deliver the real presented frame, while keeping stagingTex as our safe fallback.
		if attempt >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !haveFrame || stagingTex == nil {
		return nil, 0, fmt.Errorf("winapi: AcquireNextFrame timed out")
	}

	var mapped d3d11MappedSubresource
	rm, _, _ := syscall.SyscallN(
		ctx.vtblSlot(vtCtxMap),
		uintptr(unsafe.Pointer(ctx)),
		uintptr(unsafe.Pointer(stagingTex)),
		0,
		d3d11MapRead,
		0,
		uintptr(unsafe.Pointer(&mapped)),
	)
	if int32(uint32(rm)) < 0 || mapped.Data == 0 {
		return nil, desc.Format, fmt.Errorf("winapi: ID3D11DeviceContext::Map failed: 0x%08X", uint32(rm))
	}
	defer syscall.SyscallN(
		ctx.vtblSlot(vtCtxUnmap),
		uintptr(unsafe.Pointer(ctx)),
		uintptr(unsafe.Pointer(stagingTex)),
		0,
	)

	if desc.Format != dxgiFormatR16G16B16A16Float {
		return nil, desc.Format, nil
	}

	w, h := int(desc.Width), int(desc.Height)
	rowElems := w * 4 // 4 uint16 (8 bytes) per pixel
	rowBytes := uint32(rowElems * 2)
	if mapped.RowPitch < rowBytes {
		return nil, desc.Format, fmt.Errorf("winapi: unexpected RowPitch %d < %d", mapped.RowPitch, rowBytes)
	}

	out := make([]uint16, w*h*4)
	basePtr := *(*unsafe.Pointer)(unsafe.Pointer(&mapped.Data))
	for y := 0; y < h; y++ {
		srcRowPtr := unsafe.Add(basePtr, uintptr(y)*uintptr(mapped.RowPitch))
		srcRow := unsafe.Slice((*uint16)(srcRowPtr), rowElems)
		dstOff := y * rowElems
		copy(out[dstOff:dstOff+rowElems], srcRow)
	}

	return &HDRFrame{
		Width:        w,
		Height:       h,
		SDRWhiteNits: effectiveSDRWhiteNits(mon.SDRWhiteNits),
		Float16:      out,
	}, desc.Format, nil
}
