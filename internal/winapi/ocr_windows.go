//go:build windows

package winapi

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	combase = windows.NewLazySystemDLL("combase.dll")

	procRoInitialize              = combase.NewProc("RoInitialize")
	procRoUninitialize            = combase.NewProc("RoUninitialize")
	procRoGetActivationFactory    = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString       = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString       = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuffer = combase.NewProc("WindowsGetStringRawBuffer")
)

const (
	roInitMultithreaded = 1
	hrOK                = 0
	hrFalse             = 1
	hrRPCChangedMode    = 0x80010106

	bitmapPixelFormatBgra8   = 87
	bitmapAlphaModeIgnore    = 2
	asyncStatusStarted       = 0
	asyncStatusCompleted     = 1
	asyncStatusCanceled      = 2
	asyncStatusError         = 3
)

// WinRT interface IIDs for Buffer, SoftwareBitmap, OcrEngine, and IAsyncInfo.
var (
	iidIBufferFactory         = windows.GUID{Data1: 0x71AF914D, Data2: 0xC10F, Data3: 0x484B, Data4: [8]byte{0xBC, 0x50, 0x14, 0xBC, 0x62, 0x3B, 0x3A, 0x27}}
	iidIBufferByteAccess      = windows.GUID{Data1: 0x905A0FEF, Data2: 0xBC53, Data3: 0x11DF, Data4: [8]byte{0x8C, 0x49, 0x00, 0x1E, 0x4F, 0xC6, 0x86, 0xDA}}
	iidISoftwareBitmapStatics = windows.GUID{Data1: 0xDF0385DB, Data2: 0x672F, Data3: 0x4A9D, Data4: [8]byte{0x80, 0x6E, 0xC2, 0x44, 0x2F, 0x34, 0x3E, 0x86}}
	iidIOcrEngineStatics      = windows.GUID{Data1: 0x5BFFA85A, Data2: 0x3384, Data3: 0x3540, Data4: [8]byte{0x99, 0x40, 0x69, 0x91, 0x20, 0xD4, 0x28, 0xA8}}
	iidIAsyncInfo             = windows.GUID{Data1: 0x00000036, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIClosable              = windows.GUID{Data1: 0x30D5A829, Data2: 0x7FA4, Data3: 0x4026, Data4: [8]byte{0x83, 0xBB, 0xD7, 0x5B, 0xAE, 0x4E, 0xA9, 0x9E}}
)

// ErrOCRNoLanguage indicates Windows has no OCR language pack installed for the current profile.
var ErrOCRNoLanguage = errors.New("未检测到可用的 Windows OCR 语言包，请在系统「设置 → 时间和语言 → 语言」中安装中文或英文语言功能")

// OCRResult holds recognized text lines returned by Windows native OCR.
type OCRResult struct {
	Text  string
	Lines []string
}

type comObj struct {
	vtbl *uintptr
}

func (o *comObj) vtblSlot(slot int) uintptr {
	return *(*uintptr)(unsafe.Add(unsafe.Pointer(o.vtbl), uintptr(slot)*unsafe.Sizeof(uintptr(0))))
}

func (o *comObj) release() {
	if o == nil {
		return
	}
	syscall.SyscallN(o.vtblSlot(2), uintptr(unsafe.Pointer(o)))
}

func (o *comObj) queryInterface(iid *windows.GUID) (*comObj, uint32) {
	var out *comObj
	r, _, _ := syscall.SyscallN(o.vtblSlot(0), uintptr(unsafe.Pointer(o)), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	return out, uint32(r)
}

func (o *comObj) closeClosable() {
	if o == nil {
		return
	}
	if cl, hr := o.queryInterface(&iidIClosable); hr == hrOK && cl != nil {
		// IClosable::Close is slot 6 (after IInspectable 0..5).
		syscall.SyscallN(cl.vtblSlot(6), uintptr(unsafe.Pointer(cl)))
		cl.release()
	}
}

func createHString(s string) (uintptr, error) {
	u16, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var hs uintptr
	// Exclude trailing NUL from length parameter.
	r, _, _ := procWindowsCreateString.Call(
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(len(u16)-1),
		uintptr(unsafe.Pointer(&hs)),
	)
	if uint32(r) != hrOK {
		return 0, fmt.Errorf("WindowsCreateString(%q): 0x%08X", s, uint32(r))
	}
	return hs, nil
}

func deleteHString(hs uintptr) {
	if hs != 0 {
		procWindowsDeleteString.Call(hs)
	}
}

func hstringToGo(hs uintptr) string {
	if hs == 0 {
		return ""
	}
	var length uint32
	ptr, _, _ := procWindowsGetStringRawBuffer.Call(hs, uintptr(unsafe.Pointer(&length)))
	if ptr == 0 || length == 0 {
		return ""
	}
	// Copy UTF-16 units out without triggering go vet's uintptr->unsafe.Pointer check.
	u16 := make([]uint16, length)
	base := *(*unsafe.Pointer)(unsafe.Pointer(&ptr))
	copy(u16, unsafe.Slice((*uint16)(base), length))
	return windows.UTF16ToString(u16)
}

func getActivationFactory(className string, iid *windows.GUID) (*comObj, error) {
	hs, err := createHString(className)
	if err != nil {
		return nil, err
	}
	defer deleteHString(hs)
	var factory *comObj
	r, _, _ := procRoGetActivationFactory.Call(hs, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&factory)))
	if uint32(r) != hrOK || factory == nil {
		return nil, fmt.Errorf("RoGetActivationFactory(%s): 0x%08X", className, uint32(r))
	}
	return factory, nil
}

// RecognizeText runs Windows 10/11 built-in offline OCR (Windows.Media.Ocr) on img
// with zero external models and zero persistent memory overhead.
func RecognizeText(img *image.RGBA) (OCRResult, error) {
	if img == nil || img.Bounds().Dx() < 2 || img.Bounds().Dy() < 2 {
		return OCRResult{}, errors.New("图像为空或尺寸过小")
	}

	type ocrOut struct {
		res OCRResult
		err error
	}
	ch := make(chan ocrOut, 1)
	go func() {
		// Pin to a clean worker OS thread so RoInitialize(MTA) never conflicts with UI STA threads.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		res, err := recognizeTextOnThread(img)
		ch <- ocrOut{res: res, err: err}
	}()
	out := <-ch
	return out.res, out.err
}

func recognizeTextOnThread(img *image.RGBA) (OCRResult, error) {
	if err := combase.Load(); err != nil {
		return OCRResult{}, fmt.Errorf("无法加载系统组件 combase.dll: %w", err)
	}

	rInit, _, _ := procRoInitialize.Call(roInitMultithreaded)
	hrInit := uint32(rInit)
	needUninit := hrInit == hrOK || hrInit == hrFalse
	if !needUninit && hrInit != hrRPCChangedMode {
		return OCRResult{}, fmt.Errorf("RoInitialize 失败: 0x%08X", hrInit)
	}
	if needUninit {
		defer procRoUninitialize.Call()
	}

	// 1. Obtain IOcrEngineStatics and prefer a CJK-capable recognizer (zh-Hans
	// recognizes both Simplified Chinese and English/digits, whereas en-US
	// mangles Chinese when it sits first in Windows user profile languages).
	ocrStatics, err := getActivationFactory("Windows.Media.Ocr.OcrEngine", &iidIOcrEngineStatics)
	if err != nil {
		return OCRResult{}, fmt.Errorf("当前系统不支持 Windows 原生 OCR: %w", err)
	}
	defer ocrStatics.release()

	var maxDim uint32
	if r, _, _ := syscall.SyscallN(ocrStatics.vtblSlot(6), uintptr(unsafe.Pointer(ocrStatics)), uintptr(unsafe.Pointer(&maxDim))); uint32(r) != hrOK || maxDim == 0 {
		maxDim = 4096
	}

	engine := tryCreatePreferredOcrEngine(ocrStatics)
	if engine == nil {
		rEng, _, _ := syscall.SyscallN(ocrStatics.vtblSlot(10), uintptr(unsafe.Pointer(ocrStatics)), uintptr(unsafe.Pointer(&engine)))
		if uint32(rEng) != hrOK || engine == nil {
			return OCRResult{}, ErrOCRNoLanguage
		}
	}
	defer engine.release()

	// 2. Prepare image (upscale tiny snippets for better recognition, clamp > maxDim).
	prep := prepareOCRImage(img, int(maxDim))
	w, h := prep.Bounds().Dx(), prep.Bounds().Dy()
	byteLen := uint32(w * h * 4)

	// 3. Allocate WinRT Buffer and write BGRA8 pixels directly.
	bufFactory, err := getActivationFactory("Windows.Storage.Streams.Buffer", &iidIBufferFactory)
	if err != nil {
		return OCRResult{}, err
	}
	defer bufFactory.release()

	var ibuf *comObj
	rBuf, _, _ := syscall.SyscallN(bufFactory.vtblSlot(6), uintptr(unsafe.Pointer(bufFactory)), uintptr(byteLen), uintptr(unsafe.Pointer(&ibuf)))
	if uint32(rBuf) != hrOK || ibuf == nil {
		return OCRResult{}, fmt.Errorf("Buffer.Create 失败: 0x%08X", uint32(rBuf))
	}
	defer ibuf.release()

	// IBuffer::put_Length is slot 8.
	if rLen, _, _ := syscall.SyscallN(ibuf.vtblSlot(8), uintptr(unsafe.Pointer(ibuf)), uintptr(byteLen)); uint32(rLen) != hrOK {
		return OCRResult{}, fmt.Errorf("Buffer.put_Length 失败: 0x%08X", uint32(rLen))
	}

	byteAccess, hrBA := ibuf.queryInterface(&iidIBufferByteAccess)
	if hrBA != hrOK || byteAccess == nil {
		return OCRResult{}, fmt.Errorf("QueryInterface(IBufferByteAccess) 失败: 0x%08X", hrBA)
	}
	var rawPtr unsafe.Pointer
	rPtr, _, _ := syscall.SyscallN(byteAccess.vtblSlot(3), uintptr(unsafe.Pointer(byteAccess)), uintptr(unsafe.Pointer(&rawPtr)))
	byteAccess.release()
	if uint32(rPtr) != hrOK || rawPtr == nil {
		return OCRResult{}, fmt.Errorf("IBufferByteAccess.Buffer 失败: 0x%08X", uint32(rPtr))
	}

	dst := unsafe.Slice((*byte)(rawPtr), byteLen)
	for y := 0; y < h; y++ {
		sRow := y * prep.Stride
		dRow := y * w * 4
		for x := 0; x < w; x++ {
			so := sRow + x*4
			do := dRow + x*4
			dst[do+0] = prep.Pix[so+2] // B
			dst[do+1] = prep.Pix[so+1] // G
			dst[do+2] = prep.Pix[so+0] // R
			dst[do+3] = 255            // A (opaque)
		}
	}

	// 4. Create SoftwareBitmap via ISoftwareBitmapStatics::CreateCopyWithAlphaFromBuffer (slot 10).
	sbStatics, err := getActivationFactory("Windows.Graphics.Imaging.SoftwareBitmap", &iidISoftwareBitmapStatics)
	if err != nil {
		return OCRResult{}, err
	}
	defer sbStatics.release()

	var softBmp *comObj
	rSB, _, _ := syscall.SyscallN(
		sbStatics.vtblSlot(10),
		uintptr(unsafe.Pointer(sbStatics)),
		uintptr(unsafe.Pointer(ibuf)),
		bitmapPixelFormatBgra8,
		uintptr(w),
		uintptr(h),
		bitmapAlphaModeIgnore,
		uintptr(unsafe.Pointer(&softBmp)),
	)
	if uint32(rSB) != hrOK || softBmp == nil {
		return OCRResult{}, fmt.Errorf("SoftwareBitmap.CreateCopyWithAlphaFromBuffer 失败: 0x%08X", uint32(rSB))
	}
	defer func() {
		softBmp.closeClosable()
		softBmp.release()
	}()

	// 5. Invoke IOcrEngine::RecognizeAsync (slot 6) and wait on IAsyncInfo.
	var asyncOp *comObj
	rOp, _, _ := syscall.SyscallN(
		engine.vtblSlot(6),
		uintptr(unsafe.Pointer(engine)),
		uintptr(unsafe.Pointer(softBmp)),
		uintptr(unsafe.Pointer(&asyncOp)),
	)
	if uint32(rOp) != hrOK || asyncOp == nil {
		return OCRResult{}, fmt.Errorf("OcrEngine.RecognizeAsync 失败: 0x%08X", uint32(rOp))
	}
	defer asyncOp.release()

	asyncInfo, hrAI := asyncOp.queryInterface(&iidIAsyncInfo)
	if hrAI != hrOK || asyncInfo == nil {
		return OCRResult{}, fmt.Errorf("QueryInterface(IAsyncInfo) 失败: 0x%08X", hrAI)
	}
	defer func() {
		// IAsyncInfo::Close is slot 10.
		syscall.SyscallN(asyncInfo.vtblSlot(10), uintptr(unsafe.Pointer(asyncInfo)))
		asyncInfo.release()
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		var status int32
		rSt, _, _ := syscall.SyscallN(asyncInfo.vtblSlot(7), uintptr(unsafe.Pointer(asyncInfo)), uintptr(unsafe.Pointer(&status)))
		if uint32(rSt) != hrOK {
			return OCRResult{}, fmt.Errorf("IAsyncInfo.get_Status 失败: 0x%08X", uint32(rSt))
		}
		switch status {
		case asyncStatusCompleted:
			goto done
		case asyncStatusCanceled:
			return OCRResult{}, errors.New("文字识别已取消")
		case asyncStatusError:
			var errCode uint32
			syscall.SyscallN(asyncInfo.vtblSlot(8), uintptr(unsafe.Pointer(asyncInfo)), uintptr(unsafe.Pointer(&errCode)))
			return OCRResult{}, fmt.Errorf("OCR 识别出错: 0x%08X", errCode)
		}
		if time.Now().After(deadline) {
			syscall.SyscallN(asyncInfo.vtblSlot(9), uintptr(unsafe.Pointer(asyncInfo))) // Cancel
			return OCRResult{}, errors.New("文字识别超时")
		}
		time.Sleep(5 * time.Millisecond)
	}

done:
	// IAsyncOperation<OcrResult>::GetResults is slot 8.
	var ocrRes *comObj
	rRes, _, _ := syscall.SyscallN(asyncOp.vtblSlot(8), uintptr(unsafe.Pointer(asyncOp)), uintptr(unsafe.Pointer(&ocrRes)))
	if uint32(rRes) != hrOK || ocrRes == nil {
		return OCRResult{}, fmt.Errorf("GetResults 失败: 0x%08X", uint32(rRes))
	}
	defer ocrRes.release()

	// 6. Read lines from IOcrResult::get_Lines (slot 6) to preserve line breaks cleanly.
	var linesView *comObj
	var lines []string
	if rLV, _, _ := syscall.SyscallN(ocrRes.vtblSlot(6), uintptr(unsafe.Pointer(ocrRes)), uintptr(unsafe.Pointer(&linesView))); uint32(rLV) == hrOK && linesView != nil {
		defer linesView.release()
		var count uint32
		// IVectorView::get_Size is slot 7, GetAt is slot 6.
		if rCnt, _, _ := syscall.SyscallN(linesView.vtblSlot(7), uintptr(unsafe.Pointer(linesView)), uintptr(unsafe.Pointer(&count))); uint32(rCnt) == hrOK && count > 0 {
			lines = make([]string, 0, count)
			for i := uint32(0); i < count; i++ {
				var lineObj *comObj
				if rAt, _, _ := syscall.SyscallN(linesView.vtblSlot(6), uintptr(unsafe.Pointer(linesView)), uintptr(i), uintptr(unsafe.Pointer(&lineObj))); uint32(rAt) == hrOK && lineObj != nil {
					var hs uintptr
					// IOcrLine::get_Text is slot 7.
					if rTxt, _, _ := syscall.SyscallN(lineObj.vtblSlot(7), uintptr(unsafe.Pointer(lineObj)), uintptr(unsafe.Pointer(&hs))); uint32(rTxt) == hrOK && hs != 0 {
						cleaned := CleanOCRLine(hstringToGo(hs))
						deleteHString(hs)
						if cleaned != "" {
							lines = append(lines, cleaned)
						}
					}
					lineObj.release()
				}
			}
		}
	}

	if len(lines) == 0 {
		// Fallback to IOcrResult::get_Text (slot 8)
		var hs uintptr
		if rTxt, _, _ := syscall.SyscallN(ocrRes.vtblSlot(8), uintptr(unsafe.Pointer(ocrRes)), uintptr(unsafe.Pointer(&hs))); uint32(rTxt) == hrOK && hs != 0 {
			txt := CleanOCRLine(hstringToGo(hs))
			deleteHString(hs)
			if txt != "" {
				lines = []string{txt}
			}
		}
	}

	return OCRResult{
		Text:  strings.Join(lines, "\n"),
		Lines: lines,
	}, nil
}

// tryCreatePreferredOcrEngine inspects Windows.Media.Ocr.OcrEngine.AvailableRecognizerLanguages
// and prioritizes zh-Hans (handles both Chinese & English/digits cleanly), then zh-Hant,
// then user profile / first available recognizer.
func tryCreatePreferredOcrEngine(ocrStatics *comObj) *comObj {
	var langs *comObj
	rL, _, _ := syscall.SyscallN(ocrStatics.vtblSlot(7), uintptr(unsafe.Pointer(ocrStatics)), uintptr(unsafe.Pointer(&langs)))
	if uint32(rL) != hrOK || langs == nil {
		return nil
	}
	defer langs.release()

	var count uint32
	if rCnt, _, _ := syscall.SyscallN(langs.vtblSlot(7), uintptr(unsafe.Pointer(langs)), uintptr(unsafe.Pointer(&count))); uint32(rCnt) != hrOK || count == 0 {
		return nil
	}

	bestIdx := int32(-1)
	bestPrio := -1
	for i := uint32(0); i < count; i++ {
		var lang *comObj
		if rAt, _, _ := syscall.SyscallN(langs.vtblSlot(6), uintptr(unsafe.Pointer(langs)), uintptr(i), uintptr(unsafe.Pointer(&lang))); uint32(rAt) != hrOK || lang == nil {
			continue
		}
		// ILanguage::get_LanguageTag is slot 6 (after IInspectable 0..5).
		var hs uintptr
		tag := ""
		if rTag, _, _ := syscall.SyscallN(lang.vtblSlot(6), uintptr(unsafe.Pointer(lang)), uintptr(unsafe.Pointer(&hs))); uint32(rTag) == hrOK && hs != 0 {
			tag = strings.ToLower(hstringToGo(hs))
			deleteHString(hs)
		}
		lang.release()

		prio := 1
		switch {
		case strings.HasPrefix(tag, "zh-hans") || tag == "zh-cn":
			prio = 100
		case strings.HasPrefix(tag, "zh-hant") || tag == "zh-tw" || tag == "zh-hk":
			prio = 80
		case strings.HasPrefix(tag, "zh"):
			prio = 70
		case strings.HasPrefix(tag, "en"):
			prio = 40
		}
		if prio > bestPrio {
			bestPrio = prio
			bestIdx = int32(i)
		}
	}

	if bestIdx >= 0 {
		var lang *comObj
		if rAt, _, _ := syscall.SyscallN(langs.vtblSlot(6), uintptr(unsafe.Pointer(langs)), uintptr(bestIdx), uintptr(unsafe.Pointer(&lang))); uint32(rAt) == hrOK && lang != nil {
			var engine *comObj
			rCreate, _, _ := syscall.SyscallN(ocrStatics.vtblSlot(9), uintptr(unsafe.Pointer(ocrStatics)), uintptr(unsafe.Pointer(lang)), uintptr(unsafe.Pointer(&engine)))
			lang.release()
			if uint32(rCreate) == hrOK && engine != nil {
				return engine
			}
		}
	}
	return nil
}

// prepareOCRImage enhances desktop screenshots for Windows.Media.Ocr:
//  1. Bilinear 2× upscale (desktop 12-14px UI fonts are too small for WinRT OCR);
//  2. Auto-invert dark-background screenshots into crisp dark-text-on-light-background;
//  3. Add 24px solid background-matched border padding so edge glyphs never clip.
func prepareOCRImage(src *image.RGBA, maxDim int) *image.RGBA {
	if maxDim < 256 {
		maxDim = 4096
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	const pad = 24

	// Estimate border background luminance to detect dark-mode captures and pick padding color.
	bgR, bgG, bgB := sampleEdgeColor(src)
	darkBg := (0.2126*float64(bgR) + 0.7152*float64(bgG) + 0.0722*float64(bgB)) < 115.0

	scale := 2.0
	if sw < 160 || sh < 64 {
		scale = 2.5
	}
	maxContent := float64(maxDim - pad*2)
	if float64(sw)*scale > maxContent {
		scale = maxContent / float64(sw)
	}
	if float64(sh)*scale > maxContent {
		if s := maxContent / float64(sh); s < scale {
			scale = s
		}
	}
	if scale <= 0 {
		scale = 1.0
	}

	cw := max(1, int(float64(sw)*scale+0.5))
	ch := max(1, int(float64(sh)*scale+0.5))
	dw := cw + pad*2
	dh := ch + pad*2
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	padR, padG, padB := bgR, bgG, bgB
	if darkBg {
		padR, padG, padB = 255-padR, 255-padG, 255-padB
	}
	for i := 0; i < len(dst.Pix); i += 4 {
		dst.Pix[i+0] = padR
		dst.Pix[i+1] = padG
		dst.Pix[i+2] = padB
		dst.Pix[i+3] = 255
	}

	invScale := 1.0 / scale
	for y := 0; y < ch; y++ {
		sy := (float64(y)+0.5)*invScale - 0.5
		y0 := ClampInt(int(sy), 0, sh-1)
		y1 := ClampInt(y0+1, 0, sh-1)
		fy := sy - float64(y0)
		if fy < 0 {
			fy = 0
		}
		dRow := (y + pad) * dst.Stride
		sRow0 := y0 * src.Stride
		sRow1 := y1 * src.Stride
		for x := 0; x < cw; x++ {
			sx := (float64(x)+0.5)*invScale - 0.5
			x0 := ClampInt(int(sx), 0, sw-1)
			x1 := ClampInt(x0+1, 0, sw-1)
			fx := sx - float64(x0)
			if fx < 0 {
				fx = 0
			}
			o00 := sRow0 + x0*4
			o10 := sRow0 + x1*4
			o01 := sRow1 + x0*4
			o11 := sRow1 + x1*4
			do := dRow + (x+pad)*4
			for c := 0; c < 3; c++ {
				top := float64(src.Pix[o00+c])*(1-fx) + float64(src.Pix[o10+c])*fx
				bot := float64(src.Pix[o01+c])*(1-fx) + float64(src.Pix[o11+c])*fx
				v := uint8(ClampInt(int(top*(1-fy)+bot*fy+0.5), 0, 255))
				if darkBg {
					v = 255 - v
				}
				dst.Pix[do+c] = v
			}
			dst.Pix[do+3] = 255
		}
	}
	return dst
}

func sampleEdgeColor(src *image.RGBA) (r, g, b uint8) {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	if sw <= 0 || sh <= 0 {
		return 255, 255, 255
	}
	var sr, sg, sb, n int
	stepX := max(1, sw/24)
	stepY := max(1, sh/24)
	add := func(x, y int) {
		o := y*src.Stride + x*4
		sr += int(src.Pix[o])
		sg += int(src.Pix[o+1])
		sb += int(src.Pix[o+2])
		n++
	}
	for x := 0; x < sw; x += stepX {
		add(x, 0)
		add(x, sh-1)
	}
	for y := 0; y < sh; y += stepY {
		add(0, y)
		add(sw-1, y)
	}
	if n == 0 {
		return 255, 255, 255
	}
	return uint8(sr / n), uint8(sg / n), uint8(sb / n)
}

// CleanOCRLine removes spurious inter-character spaces Windows OCR inserts between
// CJK characters and CJK punctuation while preserving spaces in Latin/ASCII text.
func CleanOCRLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == ' ' && len(out) > 0 && i+1 < len(runes) {
			prev := out[len(out)-1]
			next := runes[i+1]
			if isCJKOrPunct(prev) && isCJKOrPunct(next) {
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}

func isCJKOrPunct(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) ||
		(r >= 0x3000 && r <= 0x303F) ||
		(r >= 0xFF00 && r <= 0xFFEF)
}
