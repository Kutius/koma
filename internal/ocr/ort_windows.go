//go:build windows

package ocr

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Frozen OrtApi function-table slot indices (verified against onnxruntime_c_api.h
// for ORT_API_VERSION >= 17; ONNX Runtime guarantees strict binary compatibility).
const (
	ortAPIVersion = 17

	ortSlotGetErrorMessage                = 2
	ortSlotCreateEnv                      = 3
	ortSlotCreateSession                  = 7
	ortSlotRun                            = 9
	ortSlotCreateSessionOptions           = 10
	ortSlotDisableCpuMemArena             = 19
	ortSlotSetSessionGraphOptLevel        = 23
	ortSlotSetIntraOpNumThreads           = 24
	ortSlotSessionGetInputName            = 36
	ortSlotSessionGetOutputName           = 37
	ortSlotCreateTensorWithDataAsOrtValue = 49
	ortSlotGetTensorMutableData           = 51
	ortSlotGetDimensionsCount             = 61
	ortSlotGetDimensions                  = 62
	ortSlotGetTensorTypeAndShape          = 65
	ortSlotCreateCpuMemoryInfo            = 69
	ortSlotAllocatorFree                  = 76
	ortSlotGetAllocatorWithDefaultOptions = 78
	ortSlotReleaseEnv                     = 92
	ortSlotReleaseStatus                  = 93
	ortSlotReleaseMemoryInfo              = 94
	ortSlotReleaseSession                 = 95
	ortSlotReleaseValue                   = 96
	ortSlotReleaseTensorTypeAndShapeInfo  = 99
	ortSlotReleaseSessionOptions          = 100
	ortSlotSessionGetModelMetadata        = 111
	ortSlotModelMetaLookupCustom          = 116
	ortSlotReleaseModelMetadata           = 118

	ortLoggingLevelError     = 3
	ortEnableAllOptimization = 99
	ortElemTypeFloat         = 1
	ortDeviceAllocator       = 1
	ortMemTypeDefault        = 0
)

type ortApiBase struct {
	getApi           uintptr
	getVersionString uintptr
}

type ortRuntime struct {
	dll       *windows.DLL
	api       unsafe.Pointer // *OrtApi
	env       uintptr
	memInfo   uintptr
	allocator uintptr
}

type ortSession struct {
	rt         *ortRuntime
	session    uintptr
	inputName  []byte // NUL-terminated C string
	outputName []byte // NUL-terminated C string
}

func loadORTRuntime(dllPath string) (*ortRuntime, error) {
	dll, err := windows.LoadDLL(dllPath)
	if err != nil {
		return nil, fmt.Errorf("加载 onnxruntime.dll 失败: %w", err)
	}
	proc, err := dll.FindProc("OrtGetApiBase")
	if err != nil {
		_ = dll.Release()
		return nil, fmt.Errorf("未找到 OrtGetApiBase: %w", err)
	}
	baseRaw, _, _ := proc.Call()
	if baseRaw == 0 {
		_ = dll.Release()
		return nil, errors.New("OrtGetApiBase 返回空指针")
	}
	base := (*ortApiBase)(*(*unsafe.Pointer)(unsafe.Pointer(&baseRaw)))
	apiRaw, _, _ := syscall.SyscallN(base.getApi, ortAPIVersion)
	if apiRaw == 0 {
		_ = dll.Release()
		return nil, fmt.Errorf("ONNX Runtime 不支持 API 版本 %d", ortAPIVersion)
	}

	rt := &ortRuntime{
		dll: dll,
		api: *(*unsafe.Pointer)(unsafe.Pointer(&apiRaw)),
	}

	logID := append([]byte("koma"), 0)
	if err := rt.checkStatus(rt.call(ortSlotCreateEnv, ortLoggingLevelError, uintptr(unsafe.Pointer(&logID[0])), uintptr(unsafe.Pointer(&rt.env)))); err != nil {
		rt.Close()
		return nil, fmt.Errorf("CreateEnv: %w", err)
	}
	if err := rt.checkStatus(rt.call(ortSlotCreateCpuMemoryInfo, ortDeviceAllocator, ortMemTypeDefault, uintptr(unsafe.Pointer(&rt.memInfo)))); err != nil {
		rt.Close()
		return nil, fmt.Errorf("CreateCpuMemoryInfo: %w", err)
	}
	if err := rt.checkStatus(rt.call(ortSlotGetAllocatorWithDefaultOptions, uintptr(unsafe.Pointer(&rt.allocator)))); err != nil {
		rt.Close()
		return nil, fmt.Errorf("GetAllocatorWithDefaultOptions: %w", err)
	}
	return rt, nil
}

func (rt *ortRuntime) slot(idx int) uintptr {
	return *(*uintptr)(unsafe.Add(rt.api, uintptr(idx)*unsafe.Sizeof(uintptr(0))))
}

func (rt *ortRuntime) call(idx int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(rt.slot(idx), args...)
	return r
}

func (rt *ortRuntime) checkStatus(status uintptr) error {
	if status == 0 {
		return nil
	}
	msgPtr := rt.call(ortSlotGetErrorMessage, status)
	msg := cStringToGo(msgPtr)
	rt.call(ortSlotReleaseStatus, status)
	if msg == "" {
		return errors.New("ONNX Runtime 内部错误")
	}
	return errors.New(msg)
}

func (rt *ortRuntime) Close() {
	if rt == nil {
		return
	}
	if rt.memInfo != 0 {
		rt.call(ortSlotReleaseMemoryInfo, rt.memInfo)
		rt.memInfo = 0
	}
	if rt.env != 0 {
		rt.call(ortSlotReleaseEnv, rt.env)
		rt.env = 0
	}
	if rt.dll != nil {
		_ = rt.dll.Release()
		rt.dll = nil
	}
}

func (rt *ortRuntime) NewSession(modelPath string) (*ortSession, error) {
	var opts uintptr
	if err := rt.checkStatus(rt.call(ortSlotCreateSessionOptions, uintptr(unsafe.Pointer(&opts)))); err != nil {
		return nil, err
	}
	defer rt.call(ortSlotReleaseSessionOptions, opts)

	threads := min(4, max(1, runtime.NumCPU()/2))
	_ = rt.checkStatus(rt.call(ortSlotSetIntraOpNumThreads, opts, uintptr(threads)))
	_ = rt.checkStatus(rt.call(ortSlotSetSessionGraphOptLevel, opts, ortEnableAllOptimization))
	_ = rt.checkStatus(rt.call(ortSlotDisableCpuMemArena, opts))

	u16Path, err := windows.UTF16PtrFromString(modelPath)
	if err != nil {
		return nil, err
	}
	var sess uintptr
	if err := rt.checkStatus(rt.call(ortSlotCreateSession, rt.env, uintptr(unsafe.Pointer(u16Path)), opts, uintptr(unsafe.Pointer(&sess)))); err != nil {
		return nil, fmt.Errorf("CreateSession(%s): %w", modelPath, err)
	}

	s := &ortSession{rt: rt, session: sess}

	var inNamePtr, outNamePtr uintptr
	if err := rt.checkStatus(rt.call(ortSlotSessionGetInputName, sess, 0, rt.allocator, uintptr(unsafe.Pointer(&inNamePtr)))); err != nil {
		s.Close()
		return nil, err
	}
	inName := cStringToGo(inNamePtr)
	_ = rt.checkStatus(rt.call(ortSlotAllocatorFree, rt.allocator, inNamePtr))

	if err := rt.checkStatus(rt.call(ortSlotSessionGetOutputName, sess, 0, rt.allocator, uintptr(unsafe.Pointer(&outNamePtr)))); err != nil {
		s.Close()
		return nil, err
	}
	outName := cStringToGo(outNamePtr)
	_ = rt.checkStatus(rt.call(ortSlotAllocatorFree, rt.allocator, outNamePtr))

	s.inputName = append([]byte(inName), 0)
	s.outputName = append([]byte(outName), 0)
	return s, nil
}

func (s *ortSession) Close() {
	if s == nil || s.session == 0 {
		return
	}
	s.rt.call(ortSlotReleaseSession, s.session)
	s.session = 0
}

func (s *ortSession) CustomMetadata(key string) (string, error) {
	var meta uintptr
	if err := s.rt.checkStatus(s.rt.call(ortSlotSessionGetModelMetadata, s.session, uintptr(unsafe.Pointer(&meta)))); err != nil {
		return "", err
	}
	defer s.rt.call(ortSlotReleaseModelMetadata, meta)

	ckey := append([]byte(key), 0)
	var valPtr uintptr
	if err := s.rt.checkStatus(s.rt.call(ortSlotModelMetaLookupCustom, meta, s.rt.allocator, uintptr(unsafe.Pointer(&ckey[0])), uintptr(unsafe.Pointer(&valPtr)))); err != nil {
		return "", err
	}
	if valPtr == 0 {
		return "", nil
	}
	val := cStringToGo(valPtr)
	_ = s.rt.checkStatus(s.rt.call(ortSlotAllocatorFree, s.rt.allocator, valPtr))
	return val, nil
}

func (s *ortSession) RunFloat32(input []float32, shape []int64) ([]float32, []int64, error) {
	if len(input) == 0 || len(shape) == 0 {
		return nil, nil, errors.New("空的推理输入张量")
	}

	var inVal uintptr
	byteLen := uintptr(len(input)) * 4
	if err := s.rt.checkStatus(s.rt.call(
		ortSlotCreateTensorWithDataAsOrtValue,
		s.rt.memInfo,
		uintptr(unsafe.Pointer(&input[0])),
		byteLen,
		uintptr(unsafe.Pointer(&shape[0])),
		uintptr(len(shape)),
		ortElemTypeFloat,
		uintptr(unsafe.Pointer(&inVal)),
	)); err != nil {
		return nil, nil, err
	}
	defer s.rt.call(ortSlotReleaseValue, inVal)

	inNames := [1]uintptr{uintptr(unsafe.Pointer(&s.inputName[0]))}
	inVals := [1]uintptr{inVal}
	outNames := [1]uintptr{uintptr(unsafe.Pointer(&s.outputName[0]))}
	var outVals [1]uintptr

	if err := s.rt.checkStatus(s.rt.call(
		ortSlotRun,
		s.session,
		0, // default run options
		uintptr(unsafe.Pointer(&inNames[0])),
		uintptr(unsafe.Pointer(&inVals[0])),
		1,
		uintptr(unsafe.Pointer(&outNames[0])),
		1,
		uintptr(unsafe.Pointer(&outVals[0])),
	)); err != nil {
		return nil, nil, err
	}
	// Keep Go slices alive across the C Run call.
	runtime.KeepAlive(input)
	runtime.KeepAlive(shape)

	outVal := outVals[0]
	if outVal == 0 {
		return nil, nil, errors.New("ONNX Runtime 返回空输出张量")
	}
	defer s.rt.call(ortSlotReleaseValue, outVal)

	var typeInfo uintptr
	if err := s.rt.checkStatus(s.rt.call(ortSlotGetTensorTypeAndShape, outVal, uintptr(unsafe.Pointer(&typeInfo)))); err != nil {
		return nil, nil, err
	}
	defer s.rt.call(ortSlotReleaseTensorTypeAndShapeInfo, typeInfo)

	var dimCount uintptr
	if err := s.rt.checkStatus(s.rt.call(ortSlotGetDimensionsCount, typeInfo, uintptr(unsafe.Pointer(&dimCount)))); err != nil {
		return nil, nil, err
	}
	outShape := make([]int64, dimCount)
	if dimCount > 0 {
		if err := s.rt.checkStatus(s.rt.call(ortSlotGetDimensions, typeInfo, uintptr(unsafe.Pointer(&outShape[0])), dimCount)); err != nil {
			return nil, nil, err
		}
	}

	totalElems := int64(1)
	for _, d := range outShape {
		if d <= 0 {
			return nil, outShape, nil
		}
		totalElems *= d
	}

	var dataPtr unsafe.Pointer
	if err := s.rt.checkStatus(s.rt.call(ortSlotGetTensorMutableData, outVal, uintptr(unsafe.Pointer(&dataPtr)))); err != nil {
		return nil, nil, err
	}
	if dataPtr == nil {
		return nil, nil, errors.New("无法读取输出张量数据")
	}

	src := unsafe.Slice((*float32)(dataPtr), totalElems)
	out := make([]float32, totalElems)
	copy(out, src)
	return out, outShape, nil
}

func cStringToGo(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	base := *(*unsafe.Pointer)(unsafe.Pointer(&ptr))
	const maxLen = 4 << 20 // 4 MiB safety cap (character dict is ~60 KB)
	raw := unsafe.Slice((*byte)(base), maxLen)
	n := 0
	for n < maxLen && raw[n] != 0 {
		n++
	}
	return string(raw[:n])
}
