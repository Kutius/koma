package winapi

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"math"
	"runtime"
	"sync"
)

// HDRFrame holds a raw Windows scRGB linear floating-point desktop capture
// (DXGI_FORMAT_R16G16B16A16_FLOAT, where 1.0 = 80 nits in BT.709 primaries).
// It is populated only for monitors that actively output HDR and released as
// soon as the capture overlay closes.
type HDRFrame struct {
	Width        int
	Height       int
	SDRWhiteNits float32  // Windows "SDR content brightness" in nits (default 80..480)
	Float16      []uint16 // 4 IEEE-754 binary16 channels per pixel (R, G, B, A)
}

// Crop extracts a compact sub-region from the HDRFrame so callers can discard
// the full-monitor float16 buffer immediately before encoding.
func (f *HDRFrame) Crop(r image.Rectangle) *HDRFrame {
	if f == nil || f.Width <= 0 || f.Height <= 0 || len(f.Float16) < f.Width*f.Height*4 {
		return nil
	}
	bounds := image.Rect(0, 0, f.Width, f.Height)
	if r.Empty() {
		r = bounds
	} else {
		r = r.Canon().Intersect(bounds)
	}
	w, h := r.Dx(), r.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	out := make([]uint16, w*h*4)
	rowElems := w * 4
	srcStride := f.Width * 4
	for y := 0; y < h; y++ {
		srcOff := (r.Min.Y+y)*srcStride + r.Min.X*4
		dstOff := y * rowElems
		copy(out[dstOff:dstOff+rowElems], f.Float16[srcOff:srcOff+rowElems])
	}
	return &HDRFrame{
		Width:        w,
		Height:       h,
		SDRWhiteNits: f.SDRWhiteNits,
		Float16:      out,
	}
}

var (
	hdrLUTOnce        sync.Once
	halfToFloatLUT    [65536]float32
	linearToSRGB8LUT  [8193]uint8
)

func initHDRLUTs() {
	hdrLUTOnce.Do(func() {
		for i := 0; i < 65536; i++ {
			halfToFloatLUT[i] = halfBitsToFloat32(uint16(i))
		}
		const maxIdx = len(linearToSRGB8LUT) - 1
		for i := 0; i <= maxIdx; i++ {
			x := float64(i) / float64(maxIdx)
			var s float64
			if x <= 0.0031308 {
				s = 12.92 * x
			} else {
				s = 1.055*math.Pow(x, 1.0/2.4) - 0.055
			}
			v := int(math.Round(s * 255.0))
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			linearToSRGB8LUT[i] = uint8(v)
		}
	})
}

// halfBitsToFloat32 decodes an IEEE-754 binary16 value into float32, clamping
// NaN/Inf to finite values safe for color math.
func halfBitsToFloat32(h uint16) float32 {
	sign := uint32(h>>15) & 1
	exp := uint32(h>>10) & 0x1F
	frac := uint32(h) & 0x03FF

	switch exp {
	case 0:
		if frac == 0 {
			return 0
		}
		// Subnormal half-float: (-1)^sign * 2^-14 * (frac / 2^10)
		v := float32(frac) * (1.0 / (16384.0 * 1024.0))
		if sign != 0 {
			return -v
		}
		return v
	case 31:
		// NaN or Inf: treat NaN / -Inf as 0, +Inf as 10000 nits (125.0 in scRGB)
		if frac != 0 || sign != 0 {
			return 0
		}
		return 125.0
	default:
		bits := (sign << 31) | ((exp + 112) << 23) | (frac << 13)
		return math.Float32frombits(bits)
	}
}

// Float32ToHalfBits encodes a float32 into IEEE-754 binary16 (used in tests and helpers).
func Float32ToHalfBits(f float32) uint16 {
	if math.IsNaN(float64(f)) {
		return 0x7E00
	}
	bits := math.Float32bits(f)
	sign := uint16((bits >> 16) & 0x8000)
	exp := int((bits>>23)&0xFF) - 127
	frac := bits & 0x007FFFFF

	if exp < -24 {
		return sign
	}
	if exp < -14 {
		// Subnormal
		mant := frac | 0x00800000
		shift := uint(-14 - exp + 13)
		return sign | uint16((mant+(1<<(shift-1)))>>shift)
	}
	if exp > 15 {
		return sign | 0x7C00 // Inf
	}
	halfExp := uint16(exp+15) << 10
	halfFrac := uint16((frac + 0x00001000) >> 13)
	if halfFrac >= 0x0400 {
		halfFrac = 0
		halfExp += 0x0400
	}
	return sign | halfExp | halfFrac
}

// effectiveSDRWhiteNits returns a clamped, sane SDR white level in nits.
func effectiveSDRWhiteNits(nits float32) float32 {
	if nits < 80 || nits > 1000 || math.IsNaN(float64(nits)) || math.IsInf(float64(nits), 0) {
		return 80.0
	}
	return nits
}

// ToneMapHDRToSDR converts a Windows scRGB float16 HDR capture into a crisp,
// SDR-faithful 8-bit sRGB *image.RGBA:
//   - Normalizes pixels by the monitor's exact Windows SDR white level so ordinary
//     SDR apps (browsers, code editors, chat windows) keep exact, clean whites.
//   - Pure SDR desktops running on an HDR monitor (peak <= 1.005) bypass shoulder
//     compression entirely for bit-accurate 1:1 sRGB reproduction.
//   - Frames containing true HDR highlights (> 1.0) apply a C1-continuous,
//     hue-preserving MaxRGB soft shoulder above an adaptive knee (0.78..1.0),
//     retaining smooth highlight roll-off without turning white UI pages gray.
func ToneMapHDRToSDR(frame *HDRFrame) *image.RGBA {
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return nil
	}
	totalPix := frame.Width * frame.Height
	if len(frame.Float16) < totalPix*4 {
		return nil
	}
	initHDRLUTs()

	sdrNits := effectiveSDRWhiteNits(frame.SDRWhiteNits)
	invSDR := float32(80.0) / sdrNits
	src := frame.Float16

	// Pass 1: fast peak detection (stride 2 on large frames to stay sub-millisecond).
	step := 1
	if totalPix > 512*512 {
		step = 2
	}
	var maxPeak float32 = 1.0
	for y := 0; y < frame.Height; y += step {
		row := y * frame.Width * 4
		for x := 0; x < frame.Width; x += step {
			off := row + x*4
			r := halfToFloatLUT[src[off+0]] * invSDR
			g := halfToFloatLUT[src[off+1]] * invSDR
			b := halfToFloatLUT[src[off+2]] * invSDR
			m := r
			if g > m {
				m = g
			}
			if b > m {
				m = b
			}
			if m > maxPeak {
				maxPeak = m
			}
		}
	}
	if maxPeak > 25.0 {
		maxPeak = 25.0
	}

	// Determine whether HDR highlight compression is needed.
	const pureSDRThreshold float32 = 1.005
	useShoulder := maxPeak > pureSDRThreshold
	var knee, headroom, shoulderK float32
	if useShoulder {
		// Smoothly slide knee from 1.0 down to 0.78 as peak rises from 1.005 to 2.0+.
		// Keeps SDR white bright (~242/255) while giving HDR highlights smooth monotonic steps.
		blend := (maxPeak - pureSDRThreshold) / (2.0 - pureSDRThreshold)
		if blend > 1.0 {
			blend = 1.0
		}
		knee = 1.0 - 0.22*blend
		headroom = 1.0 - knee
		excess := maxPeak - knee
		if excess <= headroom {
			useShoulder = false
		} else {
			// C1-continuous rational shoulder:
			//   f(knee + t) = knee + headroom * t / (headroom + t * (1 - headroom/excess))
			// Satisfies f(knee)=knee, f'(knee)=1, f(maxPeak)=1.0, strictly monotonic.
			shoulderK = 1.0 - headroom/excess
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height))
	pix := dst.Pix
	const lutScale = float32(len(linearToSRGB8LUT) - 1)
	w, h := frame.Width, frame.Height

	processRows := func(yStart, yEnd int) {
		rowStride := w * 4
		for y := yStart; y < yEnd; y++ {
			rowOff := y * rowStride
			srcRow := src[rowOff : rowOff+rowStride : rowOff+rowStride]
			pixRow := pix[rowOff : rowOff+rowStride : rowOff+rowStride]
			for off := 0; off < rowStride; off += 4 {
				r := halfToFloatLUT[srcRow[off+0]] * invSDR
				g := halfToFloatLUT[srcRow[off+1]] * invSDR
				b := halfToFloatLUT[srcRow[off+2]] * invSDR

				// Soft-desaturate wide-gamut negative BT.709 components while preserving luminance.
				minC := r
				if g < minC {
					minC = g
				}
				if b < minC {
					minC = b
				}
				if minC < 0 {
					luma := 0.2126*r + 0.7152*g + 0.0722*b
					if luma <= 0 {
						r, g, b = 0, 0, 0
					} else {
						denom := luma - minC
						if denom > 1e-6 {
							t := luma / denom
							r = luma + (r-luma)*t
							g = luma + (g-luma)*t
							b = luma + (b-luma)*t
						}
						if r < 0 {
							r = 0
						}
						if g < 0 {
							g = 0
						}
						if b < 0 {
							b = 0
						}
					}
				}

				if useShoulder {
					m := r
					if g > m {
						m = g
					}
					if b > m {
						m = b
					}
					if m > knee {
						t := m - knee
						var mapped float32
						if m >= maxPeak {
							mapped = 1.0
						} else {
							mapped = knee + headroom*t/(headroom+t*shoulderK)
						}
						scale := mapped / m
						r *= scale
						g *= scale
						b *= scale
					}
				}

				if r < 0 {
					r = 0
				} else if r > 1 {
					r = 1
				}
				if g < 0 {
					g = 0
				} else if g > 1 {
					g = 1
				}
				if b < 0 {
					b = 0
				} else if b > 1 {
					b = 1
				}

				pixRow[off+0] = linearToSRGB8LUT[int(r*lutScale+0.5)]
				pixRow[off+1] = linearToSRGB8LUT[int(g*lutScale+0.5)]
				pixRow[off+2] = linearToSRGB8LUT[int(b*lutScale+0.5)]
				pixRow[off+3] = 255
			}
		}
	}

	workers := hdrWorkerCount(totalPix, h)
	if workers <= 1 {
		processRows(0, h)
	} else {
		var wg sync.WaitGroup
		wg.Add(workers)
		for wi := 0; wi < workers; wi++ {
			y0 := wi * h / workers
			y1 := (wi + 1) * h / workers
			go func(a, b int) {
				defer wg.Done()
				processRows(a, b)
			}(y0, y1)
		}
		wg.Wait()
	}
	return dst
}

func hdrWorkerCount(totalPix, height int) int {
	if totalPix < 256*256 || height < 16 {
		return 1
	}
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		n = 8
	}
	if n > height {
		n = height
	}
	if n < 1 {
		return 1
	}
	return n
}

// SMPTE ST.2084 (PQ) OETF constants.
const (
	pqM1 = 2610.0 / 16384.0
	pqM2 = 2523.0 / 4096.0 * 128.0
	pqC1 = 3424.0 / 4096.0
	pqC2 = 2413.0 / 4096.0 * 32.0
	pqC3 = 2392.0 / 4096.0 * 32.0
)

func linearNitsToPQ16(nits float64) uint16 {
	if nits <= 0 || math.IsNaN(nits) {
		return 0
	}
	l := nits / 10000.0
	if l >= 1.0 {
		return 65535
	}
	lp := math.Pow(l, pqM1)
	v := math.Pow((pqC1+pqC2*lp)/(1.0+pqC3*lp), pqM2)
	q := int(math.Round(v * 65535.0))
	if q < 0 {
		return 0
	}
	if q > 65535 {
		return 65535
	}
	return uint16(q)
}

// EncodeHDRPNG encodes an HDRFrame (optionally cropped to crop) as a standard
// PNG Third Edition lossless 16-bit HDR PNG with an ITU-R BT.2020 + SMPTE ST.2084 PQ
// cICP chunk (primaries=9, transfer=16, matrix=0, fullRange=1).
// Supported natively by Chrome, Edge, Windows Photos, macOS Preview, and iOS.
func EncodeHDRPNG(frame *HDRFrame, crop image.Rectangle) ([]byte, error) {
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 || len(frame.Float16) < frame.Width*frame.Height*4 {
		return nil, fmt.Errorf("winapi: invalid HDR frame")
	}
	initHDRLUTs()

	bounds := image.Rect(0, 0, frame.Width, frame.Height)
	if crop.Empty() {
		crop = bounds
	} else {
		crop = crop.Canon().Intersect(bounds)
	}
	w, h := crop.Dx(), crop.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("winapi: empty HDR crop rect %v", crop)
	}

	// Convert scRGB float16 -> big-endian 16-bit RGB scanlines with PNG Sub filter (type 1, bpp=6)
	// directly in parallel, avoiding image.RGBA64 intermediate allocation and image/png's
	// per-pixel color.Color interface boxing on 16-bit images (eliminates ~2M allocs at 1080p).
	rowBytes := 1 + w*6
	filtered := make([]byte, h*rowBytes)
	src := frame.Float16
	srcStride := frame.Width * 4

	convertRows := func(yStart, yEnd int) {
		for y := yStart; y < yEnd; y++ {
			srcOff0 := (crop.Min.Y+y)*srcStride + crop.Min.X*4
			srcRow := src[srcOff0 : srcOff0+w*4 : srcOff0+w*4]
			dstRow := filtered[y*rowBytes : (y+1)*rowBytes : (y+1)*rowBytes]
			dstRow[0] = 1 // PNG filter type 1: Sub (bpp = 6 bytes)
			var prevRHi, prevRLo, prevGHi, prevGLo, prevBHi, prevBLo byte
			for x := 0; x < w; x++ {
				so := x * 4
				// scRGB linear BT.709 (1.0 = 80 nits)
				r709 := float64(halfToFloatLUT[srcRow[so+0]])
				g709 := float64(halfToFloatLUT[srcRow[so+1]])
				b709 := float64(halfToFloatLUT[srcRow[so+2]])

				// Exact ITU-R BT.709 linear -> BT.2020 linear (D65), then scale by 80 nits.
				r2020 := (0.6274039*r709 + 0.3292830*g709 + 0.0433131*b709) * 80.0
				g2020 := (0.0690973*r709 + 0.9195404*g709 + 0.0113623*b709) * 80.0
				b2020 := (0.0163914*r709 + 0.0880133*g709 + 0.8955953*b709) * 80.0

				rq := linearNitsToPQ16(r2020)
				gq := linearNitsToPQ16(g2020)
				bq := linearNitsToPQ16(b2020)

				rHi, rLo := byte(rq>>8), byte(rq)
				gHi, gLo := byte(gq>>8), byte(gq)
				bHi, bLo := byte(bq>>8), byte(bq)

				do := 1 + x*6
				dstRow[do+0] = rHi - prevRHi
				dstRow[do+1] = rLo - prevRLo
				dstRow[do+2] = gHi - prevGHi
				dstRow[do+3] = gLo - prevGLo
				dstRow[do+4] = bHi - prevBHi
				dstRow[do+5] = bLo - prevBLo

				prevRHi, prevRLo = rHi, rLo
				prevGHi, prevGLo = gHi, gLo
				prevBHi, prevBLo = bHi, bLo
			}
		}
	}

	workers := hdrWorkerCount(w*h, h)
	if workers <= 1 {
		convertRows(0, h)
	} else {
		var wg sync.WaitGroup
		wg.Add(workers)
		for wi := 0; wi < workers; wi++ {
			y0 := wi * h / workers
			y1 := (wi + 1) * h / workers
			go func(a, b int) {
				defer wg.Done()
				convertRows(a, b)
			}(y0, y1)
		}
		wg.Wait()
	}

	var idat bytes.Buffer
	idat.Grow(len(filtered) / 3)
	zw, err := zlib.NewWriterLevel(&idat, zlib.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(filtered); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	// Assemble standard PNG: signature + IHDR (16-bit RGB) + cICP (BT.2020 PQ) + IDAT + IEND.
	idatBytes := idat.Bytes()
	var out bytes.Buffer
	out.Grow(8 + 25 + 16 + 12 + len(idatBytes) + 12)
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'})

	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(h))
	ihdr[8] = 16 // bit depth: 16-bit per channel
	ihdr[9] = 2  // color type: Truecolor (RGB, opaque)
	writePNGChunk(&out, "IHDR", ihdr[:])
	writePNGChunk(&out, "cICP", []byte{9, 16, 0, 1}) // BT.2020, PQ ST.2084, RGB, Full range
	writePNGChunk(&out, "IDAT", idatBytes)
	writePNGChunk(&out, "IEND", nil)
	return out.Bytes(), nil
}

func writePNGChunk(buf *bytes.Buffer, typ string, data []byte) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(len(data)))
	copy(hdr[4:8], typ)
	buf.Write(hdr[:])
	if len(data) > 0 {
		buf.Write(data)
	}
	h := crc32.NewIEEE()
	_, _ = h.Write(hdr[4:8])
	if len(data) > 0 {
		_, _ = h.Write(data)
	}
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], h.Sum32())
	buf.Write(crcBuf[:])
}
