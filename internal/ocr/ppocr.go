package ocr

import (
	"errors"
	"fmt"
	"image"
	"math"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"koma/internal/winapi"
)

const (
	detLimitMinSide  = 736
	detLimitMaxSide  = 1600
	detProbThresh    = float32(0.30)
	detBoxThresh     = float32(0.50)
	detUnclipRatio   = 1.60
	recImgHeight     = 48
	recMinWidth      = 320
	recMaxWidth      = 2048
	recBatchSize     = 6
	recScoreThresh   = 0.50
	sessionIdleTTL   = 60 * time.Second
)

// PPOCREngine manages lazy-loaded PP-OCRv6 ONNX sessions and automatically
// releases them after sessionIdleTTL so Koma's idle tray RAM stays near zero.
type PPOCREngine struct {
	mu        sync.Mutex
	dir       string
	rt        *ortRuntime
	detSess   *ortSession
	recSess   *ortSession
	charList  []string
	idleTimer *time.Timer
}

// NewPPOCREngine creates an engine bound to the model directory dir.
func NewPPOCREngine(dir string) *PPOCREngine {
	return &PPOCREngine{dir: dir}
}

// SetDir updates the model directory, releasing any previously loaded sessions.
func (e *PPOCREngine) SetDir(dir string) {
	e.mu.Lock()
	if e.dir == dir {
		e.mu.Unlock()
		return
	}
	e.dir = dir
	e.releaseLocked()
	e.mu.Unlock()
}

// Release immediately unloads ONNX Runtime sessions and DLL handles so memory
// returns to the OS and model files can be cleanly updated or deleted.
func (e *PPOCREngine) Release() {
	e.mu.Lock()
	e.releaseLocked()
	e.mu.Unlock()
}

func (e *PPOCREngine) releaseLocked() {
	if e.idleTimer != nil {
		e.idleTimer.Stop()
		e.idleTimer = nil
	}
	if e.detSess != nil {
		e.detSess.Close()
		e.detSess = nil
	}
	if e.recSess != nil {
		e.recSess.Close()
		e.recSess = nil
	}
	if e.rt != nil {
		e.rt.Close()
		e.rt = nil
	}
	e.charList = nil
}

func (e *PPOCREngine) ensureLoadedLocked() error {
	if e.detSess != nil && e.recSess != nil && len(e.charList) > 2 {
		return nil
	}
	if !Installed(e.dir) {
		return errors.New("PP-OCRv6 模型尚未下载")
	}

	rt, err := loadORTRuntime(filepath.Join(e.dir, ortDLLFile))
	if err != nil {
		return err
	}
	detSess, err := rt.NewSession(filepath.Join(e.dir, detModelFile))
	if err != nil {
		rt.Close()
		return err
	}
	recSess, err := rt.NewSession(filepath.Join(e.dir, recModelFile))
	if err != nil {
		detSess.Close()
		rt.Close()
		return err
	}

	rawDict, err := recSess.CustomMetadata("character")
	if err != nil || strings.TrimSpace(rawDict) == "" {
		recSess.Close()
		detSess.Close()
		rt.Close()
		return fmt.Errorf("无法读取 PP-OCRv6 内置字符表: %w", err)
	}

	chars := parseCharacterList(rawDict)
	e.rt = rt
	e.detSess = detSess
	e.recSess = recSess
	e.charList = chars
	return nil
}

func (e *PPOCREngine) bumpIdleTimerLocked() {
	if e.idleTimer != nil {
		e.idleTimer.Reset(sessionIdleTTL)
		return
	}
	e.idleTimer = time.AfterFunc(sessionIdleTTL, func() {
		e.Release()
		runtime.GC()
		debug.FreeOSMemory()
	})
}

// Recognize runs full PP-OCRv6 text detection + batched CTC recognition on img.
func (e *PPOCREngine) Recognize(img *image.RGBA) (winapi.OCRResult, error) {
	if img == nil || img.Bounds().Dx() < 2 || img.Bounds().Dy() < 2 {
		return winapi.OCRResult{}, errors.New("图像为空或尺寸过小")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.ensureLoadedLocked(); err != nil {
		return winapi.OCRResult{}, err
	}
	e.bumpIdleTimerLocked()

	// Pad border gently so edge glyphs are detected cleanly by DBNet.
	const pad = 16
	padded := padImageBorder(img, pad)
	pw, ph := padded.Bounds().Dx(), padded.Bounds().Dy()

	// 1. Text Detection (PP-OCRv6 DBNet)
	detTensor, detW, detH := preprocessDet(padded)
	detOut, detShape, err := e.detSess.RunFloat32(detTensor, []int64{1, 3, int64(detH), int64(detW)})
	if err != nil {
		return winapi.OCRResult{}, fmt.Errorf("PP-OCRv6 文本检测失败: %w", err)
	}
	outH, outW := detH, detW
	if len(detShape) >= 4 {
		outH, outW = int(detShape[2]), int(detShape[3])
	}

	boxes := extractTextBoxes(detOut, outW, outH, pw, ph)
	if len(boxes) == 0 {
		// Fallback for single tight-cropped text snippets: try recognizing whole crop directly.
		if pw > ph && ph <= 160 {
			boxes = []textBox{{X0: pad, Y0: pad, X1: pw - pad, Y1: ph - pad, Score: 1.0}}
		} else {
			return winapi.OCRResult{}, nil
		}
	}

	// 2. Crop each detected text box
	crops := make([]*image.RGBA, len(boxes))
	for i, b := range boxes {
		crops[i] = cropBoxRGBA(padded, b)
	}

	// 3. Batched CTC Text Recognition (PP-OCRv6 Rec)
	texts, scores, err := e.recognizeCropsLocked(crops)
	if err != nil {
		return winapi.OCRResult{}, fmt.Errorf("PP-OCRv6 文本识别失败: %w", err)
	}

	// 4. Filter low-confidence noise and assemble into natural reading-order lines
	type recognizedBox struct {
		box  textBox
		text string
	}
	var kept []recognizedBox
	for i := range boxes {
		txt := strings.TrimSpace(texts[i])
		if txt == "" || scores[i] < recScoreThresh {
			continue
		}
		kept = append(kept, recognizedBox{box: boxes[i], text: txt})
	}
	if len(kept) == 0 {
		return winapi.OCRResult{}, nil
	}

	lines := assembleReadingLines(kept)
	return winapi.OCRResult{
		Text:  strings.Join(lines, "\n"),
		Lines: lines,
	}, nil
}

func (e *PPOCREngine) recognizeCropsLocked(crops []*image.RGBA) ([]string, []float32, error) {
	n := len(crops)
	texts := make([]string, n)
	scores := make([]float32, n)
	if n == 0 {
		return texts, scores, nil
	}

	// Sort by aspect ratio (w/h) to minimize padding overhead within each batch.
	order := make([]int, n)
	ratios := make([]float64, n)
	for i, c := range crops {
		order[i] = i
		w, h := max(1, c.Bounds().Dx()), max(1, c.Bounds().Dy())
		ratios[i] = float64(w) / float64(h)
	}
	sort.Slice(order, func(i, j int) bool {
		return ratios[order[i]] < ratios[order[j]]
	})

	for start := 0; start < n; start += recBatchSize {
		end := min(n, start+recBatchSize)
		batchN := end - start

		maxRatio := float64(recMinWidth) / float64(recImgHeight)
		for k := start; k < end; k++ {
			if r := ratios[order[k]]; r > maxRatio {
				maxRatio = r
			}
		}
		batchW := int(math.Ceil(float64(recImgHeight) * maxRatio))
		if batchW < recMinWidth {
			batchW = recMinWidth
		}
		if batchW > recMaxWidth {
			batchW = recMaxWidth
		}

		planeSize := recImgHeight * batchW
		itemSize := 3 * planeSize
		batchTensor := make([]float32, batchN*itemSize)

		for bi := 0; bi < batchN; bi++ {
			crop := crops[order[start+bi]]
			cw, ch := max(1, crop.Bounds().Dx()), max(1, crop.Bounds().Dy())
			resizedW := int(math.Ceil(float64(recImgHeight) * float64(cw) / float64(ch)))
			if resizedW < 1 {
				resizedW = 1
			}
			if resizedW > batchW {
				resizedW = batchW
			}
			fillRecCropBGR(crop, batchTensor[bi*itemSize:(bi+1)*itemSize], resizedW, batchW, recImgHeight)
		}

		out, outShape, err := e.recSess.RunFloat32(batchTensor, []int64{int64(batchN), 3, recImgHeight, int64(batchW)})
		if err != nil {
			return nil, nil, err
		}
		if len(outShape) < 3 {
			return nil, nil, fmt.Errorf("异常的识别输出形状: %v", outShape)
		}
		timeSteps := int(outShape[1])
		numClasses := int(outShape[2])

		for bi := 0; bi < batchN; bi++ {
			slice := out[bi*timeSteps*numClasses : (bi+1)*timeSteps*numClasses]
			txt, sc := decodeCTC(slice, timeSteps, numClasses, e.charList)
			origIdx := order[start+bi]
			texts[origIdx] = txt
			scores[origIdx] = sc
		}
	}

	return texts, scores, nil
}

// parseCharacterList reproduces RapidOCR CTCLabelDecode's exact dictionary indexing:
// index 0 = "blank", indices 1..N = metadata lines, index N+1 = " " (space).
func parseCharacterList(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimRight(raw, "\n")
	lines := strings.Split(raw, "\n")
	chars := make([]string, 0, len(lines)+2)
	chars = append(chars, "") // index 0 = CTC blank
	chars = append(chars, lines...)
	chars = append(chars, " ") // trailing space token per RapidOCR CTCLabelDecode
	return chars
}

// decodeCTC performs greedy argmax CTC decoding with consecutive duplicate collapse.
func decodeCTC(probs []float32, timeSteps, numClasses int, charList []string) (string, float32) {
	if timeSteps <= 0 || numClasses <= 0 || len(probs) < timeSteps*numClasses {
		return "", 0
	}
	var sb strings.Builder
	var scoreSum float64
	var count int
	prevIdx := -1

	for t := 0; t < timeSteps; t++ {
		row := probs[t*numClasses : (t+1)*numClasses]
		bestIdx := 0
		bestVal := row[0]
		for c := 1; c < numClasses; c++ {
			if row[c] > bestVal {
				bestVal = row[c]
				bestIdx = c
			}
		}
		if bestIdx != 0 && bestIdx != prevIdx {
			if bestIdx < len(charList) {
				sb.WriteString(charList[bestIdx])
				scoreSum += float64(bestVal)
				count++
			}
		}
		prevIdx = bestIdx
	}

	if count == 0 {
		return "", 0
	}
	return sb.String(), float32(scoreSum / float64(count))
}

type textBox struct {
	X0, Y0, X1, Y1 int
	Score          float32
}

func (b textBox) width() int  { return b.X1 - b.X0 }
func (b textBox) height() int { return b.Y1 - b.Y0 }

// preprocessDet resizes padded image to multiples of 32 and encodes BGR float32 NCHW.
func preprocessDet(src *image.RGBA) ([]float32, int, int) {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	ratio := 1.0
	minSide := min(sw, sh)
	maxSide := max(sw, sh)

	if minSide < detLimitMinSide {
		ratio = float64(detLimitMinSide) / float64(minSide)
		if ratio > 2.0 {
			ratio = 2.0
		}
	}
	if float64(maxSide)*ratio > float64(detLimitMaxSide) {
		ratio = float64(detLimitMaxSide) / float64(maxSide)
	}

	rw := max(32, int(math.Round(float64(sw)*ratio/32.0))*32)
	rh := max(32, int(math.Round(float64(sh)*ratio/32.0))*32)

	plane := rw * rh
	out := make([]float32, 3*plane)
	invX := float64(sw) / float64(rw)
	invY := float64(sh) / float64(rh)

	for y := 0; y < rh; y++ {
		sy := (float64(y)+0.5)*invY - 0.5
		y0 := clampInt(int(math.Floor(sy)), 0, sh-1)
		y1 := clampInt(y0+1, 0, sh-1)
		fy := sy - float64(y0)
		if fy < 0 {
			fy = 0
		}
		sRow0 := y0 * src.Stride
		sRow1 := y1 * src.Stride
		dRow := y * rw

		for x := 0; x < rw; x++ {
			sx := (float64(x)+0.5)*invX - 0.5
			x0 := clampInt(int(math.Floor(sx)), 0, sw-1)
			x1 := clampInt(x0+1, 0, sw-1)
			fx := sx - float64(x0)
			if fx < 0 {
				fx = 0
			}
			o00 := sRow0 + x0*4
			o10 := sRow0 + x1*4
			o01 := sRow1 + x0*4
			o11 := sRow1 + x1*4

			r := bilerp(src.Pix[o00+0], src.Pix[o10+0], src.Pix[o01+0], src.Pix[o11+0], fx, fy)
			g := bilerp(src.Pix[o00+1], src.Pix[o10+1], src.Pix[o01+1], src.Pix[o11+1], fx, fy)
			b := bilerp(src.Pix[o00+2], src.Pix[o10+2], src.Pix[o01+2], src.Pix[o11+2], fx, fy)

			idx := dRow + x
			// PaddleOCR / RapidOCR expects BGR channel order normalized to [-1, 1].
			out[0*plane+idx] = float32(b*(1.0/127.5) - 1.0)
			out[1*plane+idx] = float32(g*(1.0/127.5) - 1.0)
			out[2*plane+idx] = float32(r*(1.0/127.5) - 1.0)
		}
	}
	return out, rw, rh
}

func fillRecCropBGR(crop *image.RGBA, dst []float32, resizedW, batchW, targetH int) {
	sw, sh := crop.Bounds().Dx(), crop.Bounds().Dy()
	plane := targetH * batchW
	invX := float64(sw) / float64(resizedW)
	invY := float64(sh) / float64(targetH)

	for y := 0; y < targetH; y++ {
		sy := (float64(y)+0.5)*invY - 0.5
		y0 := clampInt(int(math.Floor(sy)), 0, sh-1)
		y1 := clampInt(y0+1, 0, sh-1)
		fy := sy - float64(y0)
		if fy < 0 {
			fy = 0
		}
		sRow0 := y0 * crop.Stride
		sRow1 := y1 * crop.Stride
		dRow := y * batchW

		for x := 0; x < resizedW; x++ {
			sx := (float64(x)+0.5)*invX - 0.5
			x0 := clampInt(int(math.Floor(sx)), 0, sw-1)
			x1 := clampInt(x0+1, 0, sw-1)
			fx := sx - float64(x0)
			if fx < 0 {
				fx = 0
			}
			o00 := sRow0 + x0*4
			o10 := sRow0 + x1*4
			o01 := sRow1 + x0*4
			o11 := sRow1 + x1*4

			r := bilerp(crop.Pix[o00+0], crop.Pix[o10+0], crop.Pix[o01+0], crop.Pix[o11+0], fx, fy)
			g := bilerp(crop.Pix[o00+1], crop.Pix[o10+1], crop.Pix[o01+1], crop.Pix[o11+1], fx, fy)
			b := bilerp(crop.Pix[o00+2], crop.Pix[o10+2], crop.Pix[o01+2], crop.Pix[o11+2], fx, fy)

			idx := dRow + x
			dst[0*plane+idx] = float32(b*(1.0/127.5) - 1.0)
			dst[1*plane+idx] = float32(g*(1.0/127.5) - 1.0)
			dst[2*plane+idx] = float32(r*(1.0/127.5) - 1.0)
		}
	}
}

// extractTextBoxes implements DBNet post-processing in pure Go:
// threshold -> 3x3 dilation -> 8-connected components -> probability score gate -> DB unclip.
func extractTextBoxes(probMap []float32, mapW, mapH, origW, origH int) []textBox {
	if mapW <= 0 || mapH <= 0 || len(probMap) < mapW*mapH {
		return nil
	}
	n := mapW * mapH
	bin := make([]uint8, n)
	for i := 0; i < n; i++ {
		if probMap[i] > detProbThresh {
			bin[i] = 1
		}
	}

	// 3x3 dilation matches RapidOCR's `use_dilation: true` for PP-OCRv6.
	dilated := make([]uint8, n)
	for y := 0; y < mapH; y++ {
		yMin, yMax := max(0, y-1), min(mapH-1, y+1)
		for x := 0; x < mapW; x++ {
			if bin[y*mapW+x] == 0 {
				continue
			}
			xMin, xMax := max(0, x-1), min(mapW-1, x+1)
			for yy := yMin; yy <= yMax; yy++ {
				row := yy * mapW
				for xx := xMin; xx <= xMax; xx++ {
					dilated[row+xx] = 1
				}
			}
		}
	}

	visited := make([]bool, n)
	queue := make([]int, 0, 256)
	scaleX := float64(origW) / float64(mapW)
	scaleY := float64(origH) / float64(mapH)

	var boxes []textBox
	for y := 0; y < mapH; y++ {
		for x := 0; x < mapW; x++ {
			start := y*mapW + x
			if dilated[start] == 0 || visited[start] {
				continue
			}
			visited[start] = true
			queue = append(queue[:0], start)

			minX, maxX := x, x
			minY, maxY := y, y
			var probSum float64
			var probCnt int

			for head := 0; head < len(queue); head++ {
				cur := queue[head]
				cx := cur % mapW
				cy := cur / mapW
				if cx < minX {
					minX = cx
				}
				if cx > maxX {
					maxX = cx
				}
				if cy < minY {
					minY = cy
				}
				if cy > maxY {
					maxY = cy
				}
				probSum += float64(probMap[cur])
				probCnt++

				for dy := -1; dy <= 1; dy++ {
					ny := cy + dy
					if ny < 0 || ny >= mapH {
						continue
					}
					row := ny * mapW
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						nx := cx + dx
						if nx < 0 || nx >= mapW {
							continue
						}
						ni := row + nx
						if dilated[ni] != 0 && !visited[ni] {
							visited[ni] = true
							queue = append(queue, ni)
						}
					}
				}
			}

			bw := float64(maxX - minX + 1)
			bh := float64(maxY - minY + 1)
			if min(bw, bh) < 3 {
				continue
			}

			// Compute fast box score over the bounding box (matches RapidOCR score_mode: fast)
			var boxSum float64
			for yy := minY; yy <= maxY; yy++ {
				row := yy * mapW
				for xx := minX; xx <= maxX; xx++ {
					boxSum += float64(probMap[row+xx])
				}
			}
			boxScore := float32(boxSum / (bw * bh))
			compScore := float32(probSum / float64(max(1, probCnt)))
			score := max(boxScore, compScore)
			if score < detBoxThresh {
				continue
			}

			// DBNet unclip expansion: d = area * unclip_ratio / perimeter
			area := bw * bh
			perim := 2.0 * (bw + bh)
			dist := (area * detUnclipRatio) / max(1.0, perim)
			padX := dist * 1.10
			padY := dist * 0.90

			ox0 := clampInt(int(math.Floor((float64(minX)-padX)*scaleX)), 0, origW-1)
			oy0 := clampInt(int(math.Floor((float64(minY)-padY)*scaleY)), 0, origH-1)
			ox1 := clampInt(int(math.Ceil((float64(maxX+1)+padX)*scaleX)), ox0+1, origW)
			oy1 := clampInt(int(math.Ceil((float64(maxY+1)+padY)*scaleY)), oy0+1, origH)
			if ox1-ox0 < 4 || oy1-oy0 < 4 {
				continue
			}
			boxes = append(boxes, textBox{X0: ox0, Y0: oy0, X1: ox1, Y1: oy1, Score: score})
			if len(boxes) >= 1000 {
				return boxes
			}
		}
	}
	return boxes
}

type textBoxWithText struct {
	box  textBox
	text string
}

func assembleReadingLines[T interface {
	~struct {
		box  textBox
		text string
	}
}](items []T) []string {
	type item struct {
		box  textBox
		text string
	}
	all := make([]item, len(items))
	for i, it := range items {
		v := struct {
			box  textBox
			text string
		}(it)
		all[i] = item{box: v.box, text: v.text}
	}

	// Sort top-to-bottom by vertical center first
	sort.Slice(all, func(i, j int) bool {
		ci := (all[i].box.Y0 + all[i].box.Y1)
		cj := (all[j].box.Y0 + all[j].box.Y1)
		if ci != cj {
			return ci < cj
		}
		return all[i].box.X0 < all[j].box.X0
	})

	type rowGroup struct {
		y0, y1 int
		items  []item
	}
	var rows []rowGroup
	for _, it := range all {
		placed := false
		for ri := range rows {
			ovTop := max(rows[ri].y0, it.box.Y0)
			ovBot := min(rows[ri].y1, it.box.Y1)
			ov := ovBot - ovTop
			minH := min(rows[ri].y1-rows[ri].y0, it.box.height())
			if minH > 0 && float64(ov)/float64(minH) >= 0.45 {
				rows[ri].items = append(rows[ri].items, it)
				if it.box.Y0 < rows[ri].y0 {
					rows[ri].y0 = it.box.Y0
				}
				if it.box.Y1 > rows[ri].y1 {
					rows[ri].y1 = it.box.Y1
				}
				placed = true
				break
			}
		}
		if !placed {
			rows = append(rows, rowGroup{y0: it.box.Y0, y1: it.box.Y1, items: []item{it}})
		}
	}

	// Order rows top-to-bottom and items within each row left-to-right.
	sort.Slice(rows, func(i, j int) bool { return rows[i].y0 < rows[j].y0 })
	lines := make([]string, 0, len(rows))
	for _, rg := range rows {
		sort.Slice(rg.items, func(i, j int) bool { return rg.items[i].box.X0 < rg.items[j].box.X0 })
		var sb strings.Builder
		for idx, it := range rg.items {
			if idx > 0 {
				prev := rg.items[idx-1]
				gap := it.box.X0 - prev.box.X1
				avgH := float64(prev.box.height()+it.box.height()) * 0.5
				if shouldInsertGapSpace(prev.text, it.text, gap, avgH) {
					sb.WriteByte(' ')
				}
			}
			sb.WriteString(it.text)
		}
		if line := strings.TrimSpace(sb.String()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func shouldInsertGapSpace(left, right string, gapPx int, avgH float64) bool {
	if float64(gapPx) > avgH*0.35 {
		return true
	}
	lr := []rune(left)
	rr := []rune(right)
	if len(lr) == 0 || len(rr) == 0 {
		return false
	}
	lastL := lr[len(lr)-1]
	firstR := rr[0]
	// Adjacent CJK segments with tiny gap should stay contiguous.
	if unicode.In(lastL, unicode.Han) && unicode.In(firstR, unicode.Han) {
		return false
	}
	return true
}

func padImageBorder(src *image.RGBA, pad int) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := sw+pad*2, sh+pad*2
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	// Sample average edge color so padding matches light or dark UI seamlessly.
	var sr, sg, sb, n int
	stepX := max(1, sw/16)
	stepY := max(1, sh/16)
	for x := 0; x < sw; x += stepX {
		o0 := x * 4
		o1 := (sh-1)*src.Stride + x*4
		sr += int(src.Pix[o0]) + int(src.Pix[o1])
		sg += int(src.Pix[o0+1]) + int(src.Pix[o1+1])
		sb += int(src.Pix[o0+2]) + int(src.Pix[o1+2])
		n += 2
	}
	for y := 0; y < sh; y += stepY {
		o0 := y * src.Stride
		o1 := y*src.Stride + (sw-1)*4
		sr += int(src.Pix[o0]) + int(src.Pix[o1])
		sg += int(src.Pix[o0+1]) + int(src.Pix[o1+1])
		sb += int(src.Pix[o0+2]) + int(src.Pix[o1+2])
		n += 2
	}
	br, bg, bb := uint8(255), uint8(255), uint8(255)
	if n > 0 {
		br, bg, bb = uint8(sr/n), uint8(sg/n), uint8(sb/n)
	}
	for i := 0; i < len(dst.Pix); i += 4 {
		dst.Pix[i+0] = br
		dst.Pix[i+1] = bg
		dst.Pix[i+2] = bb
		dst.Pix[i+3] = 255
	}
	for y := 0; y < sh; y++ {
		sOff := y * src.Stride
		dOff := (y+pad)*dst.Stride + pad*4
		copy(dst.Pix[dOff:dOff+sw*4], src.Pix[sOff:sOff+sw*4])
	}
	return dst
}

func cropBoxRGBA(src *image.RGBA, b textBox) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	x0 := clampInt(b.X0, 0, sw-1)
	y0 := clampInt(b.Y0, 0, sh-1)
	x1 := clampInt(b.X1, x0+1, sw)
	y1 := clampInt(b.Y1, y0+1, sh)
	w, h := x1-x0, y1-y0
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sOff := (y0+y)*src.Stride + x0*4
		dOff := y * dst.Stride
		copy(dst.Pix[dOff:dOff+w*4], src.Pix[sOff:sOff+w*4])
	}
	return dst
}

func bilerp(c00, c10, c01, c11 uint8, fx, fy float64) float64 {
	top := float64(c00)*(1-fx) + float64(c10)*fx
	bot := float64(c01)*(1-fx) + float64(c11)*fx
	return top*(1-fy) + bot*fy
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
