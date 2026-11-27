package ocr

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Engine identifies which offline OCR backend Koma uses.
type Engine string

const (
	EngineSystem  Engine = "system"
	EnginePPOCRv6 Engine = "ppocrv6"
)

// NormalizeEngine canonicalizes a stored engine preference string.
func NormalizeEngine(s string) Engine {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(EnginePPOCRv6):
		return EnginePPOCRv6
	default:
		return EngineSystem
	}
}

// DisplayName returns a concise Chinese UI label for the OCR engine.
func (e Engine) DisplayName() string {
	if e == EnginePPOCRv6 {
		return "PP-OCRv6"
	}
	return "系统原生"
}

const (
	detModelFile = "PP-OCRv6_det_small.onnx"
	recModelFile = "PP-OCRv6_rec_small.onnx"
	ortDLLFile   = "onnxruntime.dll"
	ortProvFile  = "onnxruntime_providers_shared.dll"
)

type modelAsset struct {
	Name   string
	Label  string
	URL    string
	SHA256 string
	IsWhl  bool
}

func ppocrAssets() ([]modelAsset, error) {
	if runtime.GOOS != "windows" {
		return nil, errors.New("PP-OCRv6 本地引擎目前仅支持 Windows")
	}
	var ort modelAsset
	switch runtime.GOARCH {
	case "amd64":
		ort = modelAsset{
			Name:   "onnxruntime-1.32.0-win_amd64.whl",
			Label:  "ONNX Runtime 推理引擎",
			URL:    "https://files.pythonhosted.org/packages/a9/3e/27039badc39a1b01628b6b40b1ff1bf4a886e6dfb2f311c483606b7c86f7/onnxruntime-1.32.0-cp312-cp312-win_amd64.whl",
			SHA256: "efe7456952c243a05a53d5b8732953625144642c2f3989094277f6217f0c0c40",
			IsWhl:  true,
		}
	case "arm64":
		ort = modelAsset{
			Name:   "onnxruntime-1.32.0-win_arm64.whl",
			Label:  "ONNX Runtime 推理引擎",
			URL:    "https://files.pythonhosted.org/packages/5d/e1/2ee4e15cebe7c2868e1d9ef391450db0951e6bbb2f01b2eb926722e19397/onnxruntime-1.32.0-cp312-cp312-win_arm64.whl",
			SHA256: "64d394c69d561af3efd4d4f49dcac8f11d0ce135a20701c16d53b74056cd4cf8",
			IsWhl:  true,
		}
	default:
		return nil, fmt.Errorf("不支持的系统架构: %s", runtime.GOARCH)
	}

	return []modelAsset{
		{
			Name:   detModelFile,
			Label:  "PP-OCRv6 文本检测模型",
			URL:    "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/onnx/PP-OCRv6/det/PP-OCRv6_det_small.onnx",
			SHA256: "090f04abcd9d9a7498bc4ebf677e4cb9bdce1fe4197ddb7e529f1ef44e1ff94f",
		},
		{
			Name:   recModelFile,
			Label:  "PP-OCRv6 文本识别模型",
			URL:    "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/onnx/PP-OCRv6/rec/PP-OCRv6_rec_small.onnx",
			SHA256: "6f327246b50388f3c176ae304bd95767ea6dc0c9ae92153ef8cbe210b3c14884",
		},
		ort,
	}, nil
}

// DownloadProgress reports live progress while fetching PP-OCRv6 models and runtime.
type DownloadProgress struct {
	Stage      string
	Percent    float64 // 0..100
	ItemIndex  int
	ItemTotal  int
	Downloaded int64
	Total      int64
}

// Installed reports whether all required PP-OCRv6 model and ONNX Runtime files
// exist in dir and are non-empty.
func Installed(dir string) bool {
	if dir == "" {
		return false
	}
	for _, f := range []string{detModelFile, recModelFile, ortDLLFile} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil || info.IsDir() || info.Size() < 1024 {
			return false
		}
	}
	return true
}

// DiskUsageBytes returns the total size on disk of installed PP-OCRv6 files in dir.
func DiskUsageBytes(dir string) int64 {
	if dir == "" {
		return 0
	}
	var total int64
	for _, f := range []string{detModelFile, recModelFile, ortDLLFile, ortProvFile} {
		if info, err := os.Stat(filepath.Join(dir, f)); err == nil && !info.IsDir() {
			total += info.Size()
		}
	}
	return total
}

// Uninstall removes downloaded PP-OCRv6 models and ONNX Runtime DLLs from dir.
func Uninstall(dir string) error {
	if dir == "" {
		return nil
	}
	var firstErr error
	for _, f := range []string{detModelFile, recModelFile, ortDLLFile, ortProvFile} {
		p := filepath.Join(dir, f)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DownloadPPOCRv6 fetches, SHA256-verifies, and atomically installs PP-OCRv6
// ONNX models and Windows ONNX Runtime DLLs into dir.
func DownloadPPOCRv6(ctx context.Context, dir string, onProgress func(DownloadProgress)) error {
	if dir == "" {
		return errors.New("未配置模型存储目录")
	}
	assets, err := ppocrAssets()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建模型目录失败: %w", err)
	}

	client := &http.Client{
		Timeout: 0, // per-request context controls cancellation
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
		},
	}

	totalItems := len(assets)
	for i, asset := range assets {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Skip already verified files when resuming a partial download.
		if !asset.IsWhl {
			dest := filepath.Join(dir, asset.Name)
			if ok, _ := verifyFileSHA256(dest, asset.SHA256); ok {
				if onProgress != nil {
					onProgress(DownloadProgress{
						Stage:     asset.Label,
						Percent:   float64(i+1) * 100.0 / float64(totalItems),
						ItemIndex: i + 1,
						ItemTotal: totalItems,
					})
				}
				continue
			}
		} else if info, err := os.Stat(filepath.Join(dir, ortDLLFile)); err == nil && info.Size() > 1024*1024 {
			if onProgress != nil {
				onProgress(DownloadProgress{
					Stage:     asset.Label,
					Percent:   float64(i+1) * 100.0 / float64(totalItems),
					ItemIndex: i + 1,
					ItemTotal: totalItems,
				})
			}
			continue
		}

		tmpFile := filepath.Join(dir, asset.Name+".downloading")
		_ = os.Remove(tmpFile)

		err := downloadAndVerify(ctx, client, asset.URL, tmpFile, asset.SHA256, func(done, total int64) {
			if onProgress == nil {
				return
			}
			itemFrac := 0.0
			if total > 0 {
				itemFrac = float64(done) / float64(total)
				if itemFrac > 1 {
					itemFrac = 1
				}
			}
			overall := (float64(i) + itemFrac) * 100.0 / float64(totalItems)
			onProgress(DownloadProgress{
				Stage:      asset.Label,
				Percent:    overall,
				ItemIndex:  i + 1,
				ItemTotal:  totalItems,
				Downloaded: done,
				Total:      total,
			})
		})
		if err != nil {
			_ = os.Remove(tmpFile)
			if errors.Is(err, context.Canceled) {
				return errors.New("已取消下载")
			}
			return fmt.Errorf("下载 %s 失败: %w", asset.Label, err)
		}

		if asset.IsWhl {
			if err := extractORTDLLs(tmpFile, dir); err != nil {
				_ = os.Remove(tmpFile)
				return fmt.Errorf("解压 ONNX Runtime 失败: %w", err)
			}
			_ = os.Remove(tmpFile)
		} else {
			dest := filepath.Join(dir, asset.Name)
			_ = os.Remove(dest)
			if err := os.Rename(tmpFile, dest); err != nil {
				_ = os.Remove(tmpFile)
				return fmt.Errorf("写入 %s 失败: %w", asset.Name, err)
			}
		}
	}

	if onProgress != nil {
		onProgress(DownloadProgress{
			Stage:     "安装完成",
			Percent:   100,
			ItemIndex: totalItems,
			ItemTotal: totalItems,
		})
	}
	return nil
}

func downloadAndVerify(ctx context.Context, client *http.Client, url, tmpPath, expectedSHA256 string, onBytes func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Koma-OCR-Downloader/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	h := sha256.New()
	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 64*1024)
	lastReport := time.Now()

	for {
		if ctx.Err() != nil {
			f.Close()
			return ctx.Err()
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			_, _ = h.Write(buf[:n])
			downloaded += int64(n)
			if onBytes != nil && time.Since(lastReport) >= 80*time.Millisecond {
				onBytes(downloaded, total)
				lastReport = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if onBytes != nil {
		onBytes(downloaded, total)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if expectedSHA256 != "" && !strings.EqualFold(got, expectedSHA256) {
		return fmt.Errorf("SHA256 校验失败（期望 %s，实际 %s）", expectedSHA256[:12], got[:12])
	}
	return nil
}

func verifyFileSHA256(path, expected string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expected), nil
}

func extractORTDLLs(whlPath, destDir string) error {
	zr, err := zip.OpenReader(whlPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	want := map[string]string{
		"onnxruntime/capi/onnxruntime.dll":                  ortDLLFile,
		"onnxruntime/capi/onnxruntime_providers_shared.dll": ortProvFile,
	}
	foundMain := false

	for _, f := range zr.File {
		targetName, ok := want[f.Name]
		if !ok {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		tmpOut := filepath.Join(destDir, targetName+".tmp")
		out, err := os.Create(tmpOut)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(tmpOut)
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
		finalPath := filepath.Join(destDir, targetName)
		_ = os.Remove(finalPath)
		if err := os.Rename(tmpOut, finalPath); err != nil {
			_ = os.Remove(tmpOut)
			return err
		}
		if targetName == ortDLLFile {
			foundMain = true
		}
	}

	if !foundMain {
		return errors.New("安装包中未找到 onnxruntime.dll")
	}
	return nil
}
