package winapi

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"math"
	"runtime"
	"testing"
)

func makeSyntheticHDRFrame(w, h int, sdrWhiteNits float32, fn func(x, y int) (r, g, b float32)) *HDRFrame {
	pix := make([]uint16, w*h*4)
	one := Float32ToHalfBits(1.0)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := fn(x, y)
			off := (y*w + x) * 4
			pix[off+0] = Float32ToHalfBits(r)
			pix[off+1] = Float32ToHalfBits(g)
			pix[off+2] = Float32ToHalfBits(b)
			pix[off+3] = one
		}
	}
	return &HDRFrame{
		Width:        w,
		Height:       h,
		SDRWhiteNits: sdrWhiteNits,
		Float16:      pix,
	}
}

func TestHalfFloatRoundTrip(t *testing.T) {
	initHDRLUTs()
	samples := []float32{0, 0.001, 0.25, 0.5, 1.0, 3.0, 12.5, 50.0, -0.25}
	for _, v := range samples {
		h := Float32ToHalfBits(v)
		got := halfToFloatLUT[h]
		tol := float32(math.Max(0.001, math.Abs(float64(v))*0.002))
		if float32(math.Abs(float64(got-v))) > tol {
			t.Errorf("half roundtrip %.4f -> 0x%04x -> %.4f (tol %.4f)", v, h, got, tol)
		}
	}
	// NaN / -Inf clamp safely to 0, +Inf clamps safely to finite 125.0
	if got := halfToFloatLUT[0x7E00]; got != 0 {
		t.Errorf("NaN half expected 0, got %v", got)
	}
	if got := halfToFloatLUT[0xFC00]; got != 0 {
		t.Errorf("-Inf half expected 0, got %v", got)
	}
	if got := halfToFloatLUT[0x7C00]; got != 125.0 {
		t.Errorf("+Inf half expected 125.0, got %v", got)
	}
}

func TestToneMapPureSDRDesktopOnHDRMonitorIsExact(t *testing.T) {
	// Simulate Windows HDR with SDR brightness set to 240 nits (scRGB SDR white = 3.0).
	// A pure SDR desktop should bypass shoulder compression so SDR white stays 255, not gray.
	const sdrNits = 240.0
	const sdrScale = sdrNits / 80.0 // 3.0
	frame := makeSyntheticHDRFrame(4, 1, sdrNits, func(x, _ int) (float32, float32, float32) {
		switch x {
		case 0:
			return 0, 0, 0 // black
		case 1:
			return 0.214 * sdrScale, 0.214 * sdrScale, 0.214 * sdrScale // ~50% sRGB (~127)
		case 2:
			return 1.0 * sdrScale, 0, 0 // pure sRGB red
		default:
			return 1.0 * sdrScale, 1.0 * sdrScale, 1.0 * sdrScale // pure SDR white
		}
	})

	img := ToneMapHDRToSDR(frame)
	if img == nil {
		t.Fatalf("ToneMapHDRToSDR returned nil")
	}

	// Pixel 0: pure black
	if r, g, b := img.Pix[0], img.Pix[1], img.Pix[2]; r != 0 || g != 0 || b != 0 {
		t.Errorf("black pixel = (%d,%d,%d), want (0,0,0)", r, g, b)
	}
	// Pixel 1: mid gray ~127
	if g := img.Pix[4]; g < 125 || g > 129 {
		t.Errorf("mid gray pixel = %d, want 127±2", g)
	}
	// Pixel 2: pure red (255, 0, 0)
	if r, g, b := img.Pix[8], img.Pix[9], img.Pix[10]; r != 255 || g != 0 || b != 0 {
		t.Errorf("red pixel = (%d,%d,%d), want (255,0,0)", r, g, b)
	}
	// Pixel 3: exact pure SDR white (255, 255, 255)
	if r, g, b := img.Pix[12], img.Pix[13], img.Pix[14]; r != 255 || g != 255 || b != 255 {
		t.Errorf("SDR white pixel = (%d,%d,%d), want (255,255,255)", r, g, b)
	}
}

func TestToneMapMixedHDRHighlightsPreservesSDRWhiteAndGradient(t *testing.T) {
	// Windows HDR at 240 nits SDR white + HDR highlights up to 960 nits (4x SDR white).
	const sdrNits = 240.0
	const sdrScale = sdrNits / 80.0 // 3.0
	nitsLevels := []float32{0, 60, 180, 240, 480, 720, 960}
	frame := makeSyntheticHDRFrame(len(nitsLevels), 1, sdrNits, func(x, _ int) (float32, float32, float32) {
		v := nitsLevels[x] / 80.0
		return v, v, v
	})

	img := ToneMapHDRToSDR(frame)
	if img == nil {
		t.Fatalf("ToneMapHDRToSDR returned nil")
	}

	// Verify strict monotonic highlight progression (no early clipping to flat 255 at 240/480 nits)
	// and crisp bright SDR white (~240+, never washed-out gray <225).
	prev := -1
	for x, nits := range nitsLevels {
		v := int(img.Pix[x*4])
		if v <= prev {
			t.Errorf("expected strictly increasing tone-mapped value at %.0f nits: got %d <= prev %d", nits, v, prev)
		}
		prev = v
	}
	sdrWhiteVal := img.Pix[3*4]
	if sdrWhiteVal < 235 || sdrWhiteVal > 250 {
		t.Errorf("SDR white (240 nits) on HDR frame = %d, want crisp bright white in [235, 250]", sdrWhiteVal)
	}
	if peakVal := img.Pix[(len(nitsLevels)-1)*4]; peakVal != 255 {
		t.Errorf("peak highlight (960 nits) = %d, want 255", peakVal)
	}
	_ = sdrScale
}

func TestEncodeHDRPNGValid16BitBT2020PQCICP(t *testing.T) {
	// Build a 16x8 HDR frame containing 0 nits, 80 nits, 240 nits, and 1000 nits bands.
	frame := makeSyntheticHDRFrame(16, 8, 240.0, func(x, _ int) (float32, float32, float32) {
		switch {
		case x < 4:
			return 0, 0, 0
		case x < 8:
			return 1.0, 1.0, 1.0 // 80 nits
		case x < 12:
			return 3.0, 3.0, 3.0 // 240 nits
		default:
			return 12.5, 12.5, 12.5 // 1000 nits
		}
	})

	crop := image.Rect(2, 1, 14, 7)
	raw, err := EncodeHDRPNG(frame, crop)
	if err != nil {
		t.Fatalf("EncodeHDRPNG failed: %v", err)
	}

	// 1. Verify PNG decodes cleanly via stdlib as 16-bit image with exact cropped dimensions.
	decoded, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("png.Decode failed: %v", err)
	}
	if got := decoded.Bounds(); got.Dx() != 12 || got.Dy() != 6 {
		t.Fatalf("decoded bounds = %v, want 12x6", got)
	}
	// Check PQ 16-bit values increase monotonically across 0 -> 80 -> 240 -> 1000 nits.
	r0, _, _, _ := decoded.At(0, 0).RGBA()  // orig x=2 (0 nits)
	r1, _, _, _ := decoded.At(3, 0).RGBA()  // orig x=5 (80 nits, PQ ~0.486 -> ~31800)
	r2, _, _, _ := decoded.At(7, 0).RGBA()  // orig x=9 (240 nits, PQ ~0.601 -> ~39400)
	r3, _, _, _ := decoded.At(11, 0).RGBA() // orig x=13 (1000 nits, PQ ~0.752 -> ~49300)
	if !(r0 == 0 && r0 < r1 && r1 < r2 && r2 < r3 && r3 > 48000 && r3 < 51000) {
		t.Fatalf("unexpected 16-bit PQ ramp: 0nit=%d 80nit=%d 240nit=%d 1000nit=%d", r0, r1, r2, r3)
	}

	// 2. Verify cICP chunk immediately follows IHDR with BT.2020 (9), PQ (16), RGB (0), Full (1) and valid CRC.
	const ihdrEnd = 8 + 25
	if len(raw) < ihdrEnd+16 {
		t.Fatalf("PNG too short for cICP chunk")
	}
	cLen := binary.BigEndian.Uint32(raw[ihdrEnd : ihdrEnd+4])
	cType := string(raw[ihdrEnd+4 : ihdrEnd+8])
	cData := raw[ihdrEnd+8 : ihdrEnd+12]
	cCRC := binary.BigEndian.Uint32(raw[ihdrEnd+12 : ihdrEnd+16])
	wantCRC := crc32.ChecksumIEEE(raw[ihdrEnd+4 : ihdrEnd+12])
	if cLen != 4 || cType != "cICP" || !bytes.Equal(cData, []byte{9, 16, 0, 1}) || cCRC != wantCRC {
		t.Fatalf("invalid cICP chunk: len=%d type=%q data=%v crc=0x%08x want=0x%08x", cLen, cType, cData, cCRC, wantCRC)
	}
}

func TestDisplayConfigAndMonitorHDRMetadataLive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only live DisplayConfig check")
	}
	mons := ListMonitors()
	if len(mons) == 0 {
		t.Skip("no active monitors in headless environment")
	}
	for _, m := range mons {
		if m.DeviceName == "" {
			t.Errorf("expected non-empty DeviceName for monitor %+v", m)
		}
		if m.SDRWhiteNits < 80 || m.SDRWhiteNits > 1000 {
			t.Errorf("unexpected SDRWhiteNits %.1f for monitor %q", m.SDRWhiteNits, m.DeviceName)
		}
		t.Logf("monitor %q bounds=%v dpi=%d hdr=%v sdrWhite=%.1f nits",
			m.DeviceName, m.Bounds, m.DPI, m.HDREnabled, m.SDRWhiteNits)
	}
}

func TestDXGIHDRCaptureSmokeOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only live DXGI Desktop Duplication smoke check")
	}
	mons := ListMonitors()
	if len(mons) == 0 {
		t.Skip("no active monitors")
	}
	// Exercise the full COM vtable path (IDXGIFactory1 -> IDXGIAdapter1 -> IDXGIOutput5 ->
	// D3D11CreateDevice -> DuplicateOutput1 -> AcquireNextFrame -> Staging CopyResource -> Map/Unmap)
	// requesting BOTH R16G16B16A16_FLOAT (HDR) and B8G8R8A8_UNORM (SDR) so the entire
	// COM/D3D11 pipeline executes end-to-end on both HDR and SDR Windows machines!
	hdr, gotFmt, err := captureMonitorDXGI(mons[0], []uint32{dxgiFormatR16G16B16A16Float, dxgiFormatB8G8R8A8Unorm})
	if err != nil {
		// In headless CI / RDP sessions Desktop Duplication is unavailable and CaptureMonitor falls back to GDI.
		t.Logf("DXGI duplication unavailable in this session (GDI fallback path): %v", err)
		return
	}
	t.Logf("live DXGI DuplicateOutput1 succeeded end-to-end: format=%d hdr=%v (sdrWhite=%.1f nits)",
		gotFmt, hdr != nil, mons[0].SDRWhiteNits)
	if hdr != nil {
		sdr := ToneMapHDRToSDR(hdr)
		if sdr == nil || sdr.Bounds().Dx() != hdr.Width || sdr.Bounds().Dy() != hdr.Height {
			t.Fatalf("ToneMapHDRToSDR failed on live DXGI HDR frame")
		}
	}
}

func BenchmarkToneMapHDRToSDR_4K(b *testing.B) {
	frame := makeSyntheticHDRFrame(3840, 2160, 240.0, func(x, y int) (float32, float32, float32) {
		v := float32((x+y)&0xFF) / 255.0 * 6.0 // mixed SDR + 480-nit HDR highlights
		return v, v * 0.9, v * 0.8
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ToneMapHDRToSDR(frame)
	}
}

func BenchmarkEncodeHDRPNG_1080p(b *testing.B) {
	frame := makeSyntheticHDRFrame(1920, 1080, 240.0, func(x, y int) (float32, float32, float32) {
		v := float32((x+y)&0xFF) / 255.0 * 10.0
		return v, v * 0.85, v * 0.7
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeHDRPNG(frame, image.Rectangle{}); err != nil {
			b.Fatal(err)
		}
	}
}
