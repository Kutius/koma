//go:build !windows

package ocr

import "errors"

type ortRuntime struct{}
type ortSession struct{}

func loadORTRuntime(dllPath string) (*ortRuntime, error) {
	return nil, errors.New("PP-OCRv6 is only supported on Windows")
}

func (rt *ortRuntime) Close()                                                      {}
func (rt *ortRuntime) NewSession(modelPath string) (*ortSession, error)            { return nil, errors.New("unsupported") }
func (s *ortSession) Close()                                                       {}
func (s *ortSession) CustomMetadata(key string) (string, error)                    { return "", errors.New("unsupported") }
func (s *ortSession) RunFloat32(input []float32, shape []int64) ([]float32, []int64, error) {
	return nil, nil, errors.New("unsupported")
}
