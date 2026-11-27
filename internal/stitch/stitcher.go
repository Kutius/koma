package stitch

import (
	"image"
	"math"
)

// DefaultMaxHeight caps stitched captures below Direct3D 11's 16,384px max
// 2D texture dimension so finished long screenshots always render cleanly in
// Koma Studio and desktop Pin windows without GPU texture overflow.
const DefaultMaxHeight = 16000

// FeedStatus reports the outcome of feeding a newly captured frame into Stitcher.
type FeedStatus int

const (
	StatusInit FeedStatus = iota
	StatusStitched
	StatusUnchanged
	StatusScrolledUp
	StatusTooFast
	StatusMaxHeight
)

// FeedResult summarizes one incremental frame stitching attempt.
type FeedResult struct {
	Status      FeedStatus
	ShiftY      int
	TotalHeight int
	Confidence  float64
}

const sigCols = 64

type rowSig struct {
	rgb    [sigCols * 3]uint8
	energy uint32
}

// Stitcher incrementally stitches vertical scrolling screenshots with automatic
// sticky header/footer/scrollbar resilience, dark-mode low-texture adaptation,
// momentum-guided search, and zero-copy buffer growth.
type Stitcher struct {
	width     int
	frameH    int
	stride    int
	maxHeight int

	// body holds all stitched rows from the top of Frame 0 down to the bottom of
	// the scrollable area of prevFrame (excluding footerH sticky bottom rows).
	body  []byte
	bodyH int

	// footer holds the latest sticky bottom band (length = footerH * stride).
	footer  []byte
	footerH int

	// prevFrame keeps a private copy of the most recently accepted frame.
	prevFrame *image.RGBA
	prevSigs  []rowSig
	currSigs  []rowSig
	xs        [sigCols]int

	// x0..x1 is the horizontal inner band used for matching (excludes thin edges
	// and rightmost scrollbar).
	x0, x1 int

	framesAccepted int
	totalShift     int
	lastShiftY     int
}

// New creates a new Stitcher initialized with firstFrame.
func New(firstFrame *image.RGBA, maxHeight int) *Stitcher {
	if maxHeight <= 0 {
		maxHeight = DefaultMaxHeight
	}
	s := &Stitcher{maxHeight: maxHeight}
	if firstFrame != nil {
		s.Reset(firstFrame)
	}
	return s
}

// Reset reinitializes the stitcher with firstFrame as the starting canvas.
func (s *Stitcher) Reset(firstFrame *image.RGBA) {
	if firstFrame == nil {
		return
	}
	b := firstFrame.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 16 || h < 16 {
		return
	}

	s.width = w
	s.frameH = h
	s.stride = w * 4
	if s.maxHeight < h {
		s.maxHeight = max(h, DefaultMaxHeight)
	}

	// Horizontal matching band: skip 2px left edge and scrollbar region on the right.
	scrollMargin := min(24, max(4, w/12))
	s.x0 = min(2, w/8)
	s.x1 = max(s.x0+12, w-scrollMargin)

	span := max(1, s.x1-s.x0)
	for i := 0; i < sigCols; i++ {
		s.xs[i] = s.x0 + (i*span)/sigCols
	}

	initCapH := min(s.maxHeight, max(h*4, h+1024))
	s.body = make([]byte, h*s.stride, initCapH*s.stride)
	copyRectBytes(s.body, s.stride, firstFrame, 0, h)
	s.bodyH = h

	s.footer = s.footer[:0]
	s.footerH = 0

	s.prevFrame = image.NewRGBA(image.Rect(0, 0, w, h))
	copyRectBytes(s.prevFrame.Pix, s.prevFrame.Stride, firstFrame, 0, h)

	s.prevSigs = make([]rowSig, h)
	s.currSigs = make([]rowSig, h)
	s.computeSigs(s.prevFrame, s.prevSigs)

	s.framesAccepted = 1
	s.totalShift = 0
	s.lastShiftY = 0
}

// Release frees all retained pixel buffers so GC can reclaim memory immediately.
func (s *Stitcher) Release() {
	s.body = nil
	s.bodyH = 0
	s.footer = nil
	s.footerH = 0
	s.prevFrame = nil
	s.prevSigs = nil
	s.currSigs = nil
	s.framesAccepted = 0
	s.lastShiftY = 0
}

// Width returns the stitched image width in pixels.
func (s *Stitcher) Width() int { return s.width }

// TotalHeight returns the current stitched image height in pixels (body + footer).
func (s *Stitcher) TotalHeight() int { return s.bodyH + s.footerH }

// FramesAccepted returns how many unique frames have been stitched.
func (s *Stitcher) FramesAccepted() int { return s.framesAccepted }

// Feed analyzes currFrame relative to the last accepted frame and stitches any
// newly revealed downward content.
func (s *Stitcher) Feed(currFrame *image.RGBA) FeedResult {
	if currFrame == nil {
		return FeedResult{Status: StatusUnchanged, TotalHeight: s.TotalHeight()}
	}
	if s.prevFrame == nil {
		s.Reset(currFrame)
		return FeedResult{Status: StatusInit, TotalHeight: s.TotalHeight(), Confidence: 1.0}
	}
	b := currFrame.Bounds()
	if b.Dx() != s.width || b.Dy() != s.frameH {
		return FeedResult{Status: StatusUnchanged, TotalHeight: s.TotalHeight()}
	}
	if s.TotalHeight() >= s.maxHeight {
		return FeedResult{Status: StatusMaxHeight, TotalHeight: s.TotalHeight()}
	}

	// Compute compact signatures for currFrame once (reusing currSigs buffer).
	s.computeSigs(currFrame, s.currSigs)

	// 1. Fast unchanged check across inner matching band (tight enough to catch
	// subtle smooth-scroll steps on sparse pages while still ignoring blinking carets).
	meanDiff, changedRatio := s.frameDiff(s.prevSigs, s.currSigs)
	if meanDiff < 0.45 && changedRatio < 0.003 {
		return FeedResult{Status: StatusUnchanged, TotalHeight: s.TotalHeight()}
	}

	// Compute outlier-resistant texture-energy floor so low-contrast dark-mode UIs
	// (e.g., muted gray code on #1E1E1E, even with a bright titlebar/status bar)
	// still register textured rows accurately.
	minTexEnergy := s.adaptiveTexThreshold()

	// 2. Detect sticky top header and sticky bottom footer between prevFrame & currFrame.
	staticTop, staticBot := s.detectStickyBands(minTexEnergy)

	// Align sticky footer state conservatively so bodyH always matches prevFrame[0..botY).
	targetFooterH := s.footerH
	if s.framesAccepted == 1 {
		// On the very first scroll transition, lock detected sticky footer if substantial.
		if staticBot >= 8 {
			targetFooterH = staticBot
		}
	} else if s.footerH > 0 {
		// If a previously detected footer shrank or vanished, keep the smaller consistent band.
		if staticBot < s.footerH {
			targetFooterH = staticBot
		}
	}

	// Compute scrollable matching window [topY .. botY).
	topY := staticTop
	botY := s.frameH - max(targetFooterH, staticBot)
	minScrollH := max(48, s.frameH*35/100)
	if botY-topY < minScrollH {
		// Too much of the frame looks static (e.g. blank margins); relax conservatively.
		if staticBot > s.frameH/3 {
			staticBot = 0
			targetFooterH = min(targetFooterH, 0)
		}
		if staticTop > s.frameH/3 {
			staticTop = 0
		}
		topY = staticTop
		botY = s.frameH - max(targetFooterH, staticBot)
		if botY-topY < minScrollH {
			topY = 0
			botY = s.frameH
			targetFooterH = 0
		}
	}

	// 3. Search for downward vertical shift dy > 0 inside [topY .. botY).
	dy, bestErr, secondErr, texRows, ok := s.findDownwardShift(currFrame, topY, botY, minTexEnergy)
	if !ok {
		// Check if the user accidentally scrolled slightly upward (jitter) so we ignore cleanly.
		if s.detectUpwardScroll(currFrame, topY, botY, minTexEnergy) {
			return FeedResult{Status: StatusScrolledUp, TotalHeight: s.TotalHeight()}
		}
		return FeedResult{Status: StatusTooFast, TotalHeight: s.TotalHeight()}
	}

	// 4. Reconcile footer band with body before appending newly revealed rows.
	if targetFooterH > s.footerH {
		// First scroll discovered a sticky footer: trim those rows from body tail.
		trim := targetFooterH - s.footerH
		if trim < s.bodyH {
			s.bodyH -= trim
		}
		s.footerH = targetFooterH
	} else if targetFooterH < s.footerH {
		// Sticky footer shrank: commit released top rows of old footer back into body.
		release := s.footerH - targetFooterH
		s.ensureBodyCap(s.bodyH + release)
		copy(s.body[s.bodyH*s.stride:(s.bodyH+release)*s.stride], s.footer[:release*s.stride])
		s.bodyH += release
		s.footerH = targetFooterH
	}

	// Clamp shift if appending would exceed maxHeight.
	avail := s.maxHeight - (s.bodyH + s.footerH)
	if avail <= 0 {
		return FeedResult{Status: StatusMaxHeight, TotalHeight: s.TotalHeight()}
	}
	clampedMax := false
	if dy > avail {
		dy = avail
		clampedMax = true
	}

	// Append new rows with a small overlap trim (pad) so bottom-edge anti-aliasing
	// artifacts from prevFrame are cleanly overwritten by currFrame's interior pixels.
	scrollBot := s.frameH - s.footerH
	overlap := (scrollBot - topY) - dy
	pad := min(4, max(0, overlap/4))
	if pad >= s.bodyH {
		pad = 0
	}

	writeStartRow := s.bodyH - pad
	srcStartRow := scrollBot - dy - pad
	copyRows := dy + pad
	if srcStartRow < 0 {
		pad += srcStartRow
		writeStartRow = s.bodyH - pad
		srcStartRow = 0
		copyRows = scrollBot - srcStartRow
	}

	newBodyH := writeStartRow + copyRows
	s.ensureBodyCap(newBodyH)
	copyRectBytes(s.body[writeStartRow*s.stride:newBodyH*s.stride], s.stride, currFrame, srcStartRow, copyRows)
	s.bodyH = newBodyH

	// Refresh sticky footer pixels from currFrame.
	if s.footerH > 0 {
		need := s.footerH * s.stride
		if cap(s.footer) < need {
			s.footer = make([]byte, need)
		} else {
			s.footer = s.footer[:need]
		}
		copyRectBytes(s.footer, s.stride, currFrame, s.frameH-s.footerH, s.footerH)
	}

	// Advance prevFrame and swap signature slices (zero allocation).
	copyRectBytes(s.prevFrame.Pix, s.prevFrame.Stride, currFrame, 0, s.frameH)
	s.prevSigs, s.currSigs = s.currSigs, s.prevSigs
	s.framesAccepted++
	s.totalShift += dy
	s.lastShiftY = dy

	conf := 1.0
	if secondErr > 0 {
		conf = math.Min(1.0, secondErr/(bestErr+1.0)/3.0)
	}
	if texRows == 0 {
		conf *= 0.5
	}

	st := StatusStitched
	if clampedMax || s.TotalHeight() >= s.maxHeight {
		st = StatusMaxHeight
	}
	return FeedResult{
		Status:      st,
		ShiftY:      dy,
		TotalHeight: s.TotalHeight(),
		Confidence:  conf,
	}
}

// adaptiveTexThreshold lowers the texture-energy floor on low-contrast dark-mode
// frames while preserving the standard sigCols*3 floor on normal pages.
// Sampling the middle 60% of the viewport and requiring >=10% high-contrast rows
// prevents a single bright titlebar, status bar, or accent badge from disabling
// dark-mode adaptation.
func (s *Stitcher) adaptiveTexThreshold() uint32 {
	const defaultFloor uint32 = sigCols * 3
	const darkFloor uint32 = sigCols
	const highContrastCut uint32 = defaultFloor * 2

	yStart := s.frameH / 5
	yEnd := s.frameH - s.frameH/5
	if yEnd-yStart < 16 {
		yStart = 0
		yEnd = s.frameH
	}
	sampled := 0
	highContrast := 0
	for y := yStart; y < yEnd; y += 4 {
		sampled++
		if s.currSigs[y].energy >= highContrastCut {
			highContrast++
		}
	}
	if sampled > 0 && highContrast*10 < sampled {
		return darkFloor
	}
	return defaultFloor
}

// Compose returns a standalone *image.RGBA containing the complete stitched image.
func (s *Stitcher) Compose() *image.RGBA {
	totalH := s.TotalHeight()
	if s.width <= 0 || totalH <= 0 {
		return nil
	}
	out := image.NewRGBA(image.Rect(0, 0, s.width, totalH))
	bodyBytes := s.bodyH * s.stride
	copy(out.Pix[:bodyBytes], s.body[:bodyBytes])
	if s.footerH > 0 {
		copy(out.Pix[bodyBytes:], s.footer[:s.footerH*s.stride])
	}
	return out
}

// RenderPreview downscales the visible window of the stitched image directly
// into dst without heap allocations, returning the scaled full content height
// and visible viewport height in dst pixels.
// scrollFrac is in [0..1] where 0 shows the top and 1 shows the latest bottom.
func (s *Stitcher) RenderPreview(dst *image.RGBA, scrollFrac float32) (scaledTotalH, dstH int) {
	if dst == nil || s.width <= 0 || s.TotalHeight() <= 0 {
		return 0, 0
	}
	dw, dh := dst.Bounds().Dx(), dst.Bounds().Dy()
	if dw <= 0 || dh <= 0 {
		return 0, 0
	}

	totalH := s.TotalHeight()
	scale := float64(dw) / float64(s.width)
	scaledTotalH = max(1, int(math.Round(float64(totalH)*scale)))

	if scrollFrac < 0 {
		scrollFrac = 0
	} else if scrollFrac > 1 {
		scrollFrac = 1
	}

	maxScrollSrc := float64(totalH) - float64(dh)/scale
	if maxScrollSrc < 0 {
		maxScrollSrc = 0
	}
	srcStartY := float64(scrollFrac) * maxScrollSrc

	// Clear dst to transparent first so shorter images leave clean space below.
	clear(dst.Pix)

	drawRows := min(dh, scaledTotalH)
	invScale := float64(s.width) / float64(dw)
	bodyH := s.bodyH
	stride := s.stride

	rowPtr := func(sy int) []byte {
		if sy < bodyH {
			off := sy * stride
			return s.body[off : off+stride]
		}
		fy := sy - bodyH
		if fy >= s.footerH {
			fy = s.footerH - 1
		}
		if fy < 0 {
			return s.body[(bodyH-1)*stride : bodyH*stride]
		}
		off := fy * stride
		return s.footer[off : off+stride]
	}

	// 2x2 area supersampling per preview pixel for crisp, legible text downscaling.
	for y := 0; y < drawRows; y++ {
		sy0 := int(srcStartY + (float64(y)+0.25)*invScale)
		sy1 := int(srcStartY + (float64(y)+0.75)*invScale)
		if sy0 >= totalH {
			sy0 = totalH - 1
		}
		if sy1 >= totalH {
			sy1 = totalH - 1
		}
		r0 := rowPtr(sy0)
		r1 := rowPtr(sy1)
		dRow := dst.Pix[y*dst.Stride : y*dst.Stride+dw*4]

		for x := 0; x < dw; x++ {
			sx0 := int((float64(x) + 0.25) * invScale)
			sx1 := int((float64(x) + 0.75) * invScale)
			if sx0 >= s.width {
				sx0 = s.width - 1
			}
			if sx1 >= s.width {
				sx1 = s.width - 1
			}
			o00 := sx0 * 4
			o01 := sx1 * 4
			d := x * 4
			dRow[d+0] = uint8((uint32(r0[o00+0]) + uint32(r0[o01+0]) + uint32(r1[o00+0]) + uint32(r1[o01+0]) + 2) >> 2)
			dRow[d+1] = uint8((uint32(r0[o00+1]) + uint32(r0[o01+1]) + uint32(r1[o00+1]) + uint32(r1[o01+1]) + 2) >> 2)
			dRow[d+2] = uint8((uint32(r0[o00+2]) + uint32(r0[o01+2]) + uint32(r1[o00+2]) + uint32(r1[o01+2]) + 2) >> 2)
			dRow[d+3] = 255
		}
	}
	return scaledTotalH, dh
}

func (s *Stitcher) ensureBodyCap(needRows int) {
	needBytes := needRows * s.stride
	if len(s.body) >= needBytes {
		return
	}
	if cap(s.body) >= needBytes {
		s.body = s.body[:needBytes]
		return
	}
	newCapH := max(needRows, len(s.body)/s.stride*2)
	if newCapH > s.maxHeight {
		newCapH = s.maxHeight
	}
	if newCapH < needRows {
		newCapH = needRows
	}
	next := make([]byte, needBytes, newCapH*s.stride)
	copy(next, s.body[:s.bodyH*s.stride])
	s.body = next
}

func (s *Stitcher) computeSigs(img *image.RGBA, out []rowSig) {
	pix := img.Pix
	stride := img.Stride

	// Note: in Go's image.RGBA, SubImage already advances img.Pix to Rect.Min,
	// so row 0 / col 0 of the frame is always at Pix[0].
	for y := 0; y < s.frameH; y++ {
		rowOff := y * stride
		sig := &out[y]
		var energy uint32
		var prevL int
		for i := 0; i < sigCols; i++ {
			off := rowOff + s.xs[i]*4
			r := pix[off+0]
			g := pix[off+1]
			bl := pix[off+2]
			sig.rgb[i*3+0] = r
			sig.rgb[i*3+1] = g
			sig.rgb[i*3+2] = bl
			l := (int(r)*77 + int(g)*150 + int(bl)*29) >> 8
			if i > 0 {
				d := l - prevL
				if d < 0 {
					d = -d
				}
				energy += uint32(d)
			}
			prevL = l
		}
		sig.energy = energy
	}
}

func (s *Stitcher) frameDiff(a, b []rowSig) (meanDiff float64, changedRatio float64) {
	var totalDiff uint64
	var changed int
	totalSamples := 0
	for y := 0; y < s.frameH; y += 2 {
		ar := &a[y].rgb
		br := &b[y].rgb
		for i := 0; i < sigCols*3; i += 3 {
			dr := absDiff8(ar[i+0], br[i+0])
			dg := absDiff8(ar[i+1], br[i+1])
			db := absDiff8(ar[i+2], br[i+2])
			d := dr + dg + db
			totalDiff += uint64(d)
			if d > 18 {
				changed++
			}
			totalSamples++
		}
	}
	if totalSamples == 0 {
		return 0, 0
	}
	return float64(totalDiff) / float64(totalSamples*3), float64(changed) / float64(totalSamples)
}

func sigRowMAD(a, b *rowSig) float64 {
	var sum uint32
	ar := &a.rgb
	br := &b.rgb
	for i := 0; i < sigCols*3; i += 3 {
		sum += uint32(absDiff8(ar[i+0], br[i+0])) +
			uint32(absDiff8(ar[i+1], br[i+1])) +
			uint32(absDiff8(ar[i+2], br[i+2]))
	}
	return float64(sum) / float64(sigCols*3)
}

func (s *Stitcher) detectStickyBands(minTexEnergy uint32) (stickyTop, stickyBot int) {
	maxBand := s.frameH * 38 / 100
	if maxBand < 8 {
		return 0, 0
	}

	// Scan from top downward.
	identTop := 0
	hasTopTex := false
	for y := 0; y < maxBand; y++ {
		if sigRowMAD(&s.prevSigs[y], &s.currSigs[y]) > 2.0 {
			break
		}
		if s.prevSigs[y].energy >= minTexEnergy {
			hasTopTex = true
		}
		identTop = y + 1
	}
	if hasTopTex && identTop >= 8 {
		stickyTop = identTop
	}

	// Scan from bottom upward.
	identBot := 0
	hasBotTex := false
	for k := 0; k < maxBand; k++ {
		y := s.frameH - 1 - k
		if sigRowMAD(&s.prevSigs[y], &s.currSigs[y]) > 2.0 {
			break
		}
		if s.prevSigs[y].energy >= minTexEnergy {
			hasBotTex = true
		}
		identBot = k + 1
	}
	if hasBotTex && identBot >= 8 {
		stickyBot = identBot
	}
	return stickyTop, stickyBot
}

type shiftCand struct {
	dy  int
	err float64
}

func (s *Stitcher) findDownwardShift(curr *image.RGBA, topY, botY int, minTexEnergy uint32) (bestDy int, bestErr, secondErr float64, texRows int, ok bool) {
	scrollH := botY - topY
	minOverlap := max(24, scrollH*14/100)
	maxShift := scrollH - minOverlap
	if maxShift < 2 {
		return 0, 255, 255, 0, false
	}

	// Stage 1: Coarse search using precomputed row signatures (step = 2 rows).
	const maxCands = 8
	var cands [maxCands]shiftCand
	nCands := 0
	worstIdx := 0
	worstErr := -1.0
	addCand := func(dy int, err float64) {
		for i := 0; i < nCands; i++ {
			if cands[i].dy == dy {
				return
			}
		}
		if nCands < maxCands {
			cands[nCands] = shiftCand{dy: dy, err: err}
			if err > worstErr {
				worstErr = err
				worstIdx = nCands
			}
			nCands++
			return
		}
		if err < worstErr {
			cands[worstIdx] = shiftCand{dy: dy, err: err}
			worstIdx = 0
			worstErr = cands[0].err
			for i := 1; i < nCands; i++ {
				if cands[i].err > worstErr {
					worstErr = cands[i].err
					worstIdx = i
				}
			}
		}
	}

	// Pre-seed candidate buffer with momentum hint so worstErr tightens early.
	if s.lastShiftY >= 2 && s.lastShiftY <= maxShift {
		dy := s.lastShiftY
		ov := scrollH - dy
		rowStep := 2
		if ov > 360 {
			rowStep = 3
		}
		var sum float64
		cnt := 0
		for k := 0; k < ov; k += rowStep {
			sum += sigRowMAD(&s.prevSigs[topY+dy+k], &s.currSigs[topY+k])
			cnt++
		}
		if cnt > 0 {
			addCand(dy, sum/float64(cnt))
		}
	}

	for dy := 2; dy <= maxShift; dy += 2 {
		if dy == s.lastShiftY {
			continue
		}
		ov := scrollH - dy
		rowStep := 2
		if ov > 360 {
			rowStep = 3
		}
		totalRows := (ov + rowStep - 1) / rowStep
		var limit float64
		hasLimit := nCands == maxCands
		if hasLimit {
			limit = worstErr * float64(totalRows)
		}
		var sum float64
		cnt := 0
		aborted := false
		for k := 0; k < ov; k += rowStep {
			sum += sigRowMAD(&s.prevSigs[topY+dy+k], &s.currSigs[topY+k])
			cnt++
			if hasLimit && sum >= limit {
				aborted = true
				break
			}
		}
		if !aborted && cnt > 0 {
			addCand(dy, sum/float64(cnt))
		}
	}

	// Sort coarse candidates ascending so fine search locks onto a tight bestErr
	// immediately, maximizing early-abort pruning on poorer candidates.
	for i := 1; i < nCands; i++ {
		key := cands[i]
		j := i - 1
		for j >= 0 && cands[j].err > key.err {
			cands[j+1] = cands[j]
			j--
		}
		cands[j+1] = key
	}

	// Stack-allocated array avoids per-frame map allocations at 25fps.
	var tested [56]shiftCand
	nTested := 0
	bestErr = math.MaxFloat64
	bestDy = 0
	bestTexRows := 0

	evalFine := func(dy int) {
		if dy < 1 || dy > maxShift {
			return
		}
		for i := 0; i < nTested; i++ {
			if tested[i].dy == dy {
				return
			}
		}
		// Conservative cutoff for early abort while preserving accurate secondErr
		// discrimination for near-contenders.
		cutoff := 255.0
		if bestErr < 14.0 {
			cutoff = math.Max(18.0, bestErr*2.2+6.0)
		}
		err, tRows := s.exactOverlapMAD(curr, topY+dy, topY, scrollH-dy, minTexEnergy, cutoff)
		if nTested < len(tested) {
			tested[nTested] = shiftCand{dy: dy, err: err}
			nTested++
		}
		if err < bestErr {
			bestErr = err
			bestDy = dy
			bestTexRows = tRows
		}
	}

	// Momentum hint: probe lastShiftY neighborhood first so steady scrolling locks
	// in an immediate tight upper bound.
	if s.lastShiftY > 0 {
		evalFine(s.lastShiftY)
		evalFine(s.lastShiftY - 1)
		evalFine(s.lastShiftY + 1)
	}
	// Evaluate top coarse candidates first (sorted ascending by coarse error).
	for i := 0; i < nCands; i++ {
		base := cands[i].dy
		for delta := -2; delta <= 2; delta++ {
			evalFine(base + delta)
		}
	}
	// Always include small shifts [1..4] for precision trackpad nudges.
	for dy := 1; dy <= min(4, maxShift); dy++ {
		evalFine(dy)
	}

	if bestDy <= 0 {
		return 0, bestErr, 255, 0, false
	}

	// Compute second-best distinct shift error (at least 8px away) to gauge uniqueness.
	secondErr = math.MaxFloat64
	for i := 0; i < nTested; i++ {
		if absInt(tested[i].dy-bestDy) >= 8 && tested[i].err < secondErr {
			secondErr = tested[i].err
		}
	}
	if secondErr == math.MaxFloat64 {
		// Also probe coarse candidates far from bestDy if fine set was narrow.
		for i := 0; i < nCands; i++ {
			if absInt(cands[i].dy-bestDy) >= 8 && cands[i].err < secondErr {
				secondErr = cands[i].err
			}
		}
	}
	if secondErr == math.MaxFloat64 {
		secondErr = 255
	}

	// Acceptance thresholds: strict enough to reject bad/fast jumps, tolerant of
	// ClearType subpixel rendering and smooth-scroll momentum across scrolled text.
	if bestErr <= 7.5 {
		// Guard against completely blank uniform overlaps where every dy gives ~0 error.
		if bestTexRows == 0 && secondErr < 1.5 {
			return 0, bestErr, secondErr, bestTexRows, false
		}
		return bestDy, bestErr, secondErr, bestTexRows, true
	}
	if bestErr <= 14.0 && bestTexRows >= 4 && secondErr >= bestErr*1.55+2.5 {
		return bestDy, bestErr, secondErr, bestTexRows, true
	}
	return 0, bestErr, secondErr, bestTexRows, false
}

func (s *Stitcher) detectUpwardScroll(curr *image.RGBA, topY, botY int, minTexEnergy uint32) bool {
	scrollH := botY - topY
	maxUp := min(scrollH/2, scrollH-max(24, scrollH*20/100))
	if maxUp < 2 {
		return false
	}
	bestDy := 0
	bestCoarse := math.MaxFloat64
	for dy := 2; dy <= maxUp; dy += 2 {
		ov := scrollH - dy
		var sum float64
		cnt := 0
		for k := 0; k < ov; k += 3 {
			sum += sigRowMAD(&s.prevSigs[topY+k], &s.currSigs[topY+dy+k])
			cnt++
		}
		if cnt > 0 {
			m := sum / float64(cnt)
			if m < bestCoarse {
				bestCoarse = m
				bestDy = dy
			}
		}
	}
	if bestDy <= 0 || bestCoarse > 14.0 {
		return false
	}
	for delta := -1; delta <= 1; delta++ {
		dy := bestDy + delta
		if dy < 1 || dy > maxUp {
			continue
		}
		err, tRows := s.exactOverlapMAD(curr, topY, topY+dy, scrollH-dy, minTexEnergy, 12.0)
		if err <= 8.0 && tRows >= 4 {
			return true
		}
	}
	return false
}

// exactOverlapMAD compares prevFrame[prevStartY..prevStartY+ov) with
// currFrame[currStartY..currStartY+ov) across the inner horizontal band with
// early-abort upper-bound pruning when accumulated error already exceeds cutoff.
func (s *Stitcher) exactOverlapMAD(curr *image.RGBA, prevStartY, currStartY, ov int, minTexEnergy uint32, cutoff float64) (mad float64, texRows int) {
	if ov <= 0 {
		return 255, 0
	}
	prevPix := s.prevFrame.Pix
	prevStride := s.prevFrame.Stride
	currPix := curr.Pix
	currStride := curr.Stride

	xStep := 2
	if s.x1-s.x0 > 600 {
		xStep = 3
	}
	yStep := 1
	if ov > 500 {
		yStep = 2
	}

	x0, x1 := s.x0, s.x1
	samplesPerRow := uint64((x1 - x0 + xStep - 1) / xStep)
	totalRowsEst := uint64((ov + yStep - 1) / yStep)
	maxTotalDiff := uint64(cutoff * float64(samplesPerRow*totalRowsEst*3))
	doEarlyAbort := cutoff < 250.0

	var totalDiff uint64
	var totalPx uint64
	var texDiff uint64
	var texPx uint64
	rowsChecked := 0

	for k := 0; k < ov; k += yStep {
		py := prevStartY + k
		cy := currStartY + k
		pRow := prevPix[py*prevStride:]
		cRow := currPix[cy*currStride:]
		isTex := s.prevSigs[py].energy >= minTexEnergy || s.currSigs[cy].energy >= minTexEnergy
		var rowDiff uint64
		var rowPx uint64
		for x := x0; x < x1; x += xStep {
			off := x * 4
			p0, p1, p2 := pRow[off+0], pRow[off+1], pRow[off+2]
			c0, c1, c2 := cRow[off+0], cRow[off+1], cRow[off+2]
			d := uint64(absDiff8(p0, c0)) + uint64(absDiff8(p1, c1)) + uint64(absDiff8(p2, c2))
			rowDiff += d
			rowPx++
		}
		totalDiff += rowDiff
		totalPx += rowPx
		if isTex {
			texRows++
			texDiff += rowDiff
			texPx += rowPx
		}
		rowsChecked++
		if doEarlyAbort && rowsChecked&15 == 0 && totalDiff > maxTotalDiff {
			return cutoff + 1.0, texRows
		}
	}
	if totalPx == 0 {
		return 255, 0
	}
	allMAD := float64(totalDiff) / float64(totalPx*3)
	if texRows >= 8 && texPx > 0 {
		// Weight textured rows strongly so large flat margins cannot mask text misalignment.
		texMAD := float64(texDiff) / float64(texPx*3)
		return allMAD*0.35 + texMAD*0.65, texRows
	}
	return allMAD, texRows
}

func copyRectBytes(dst []byte, dstStride int, src *image.RGBA, srcStartY, rows int) {
	rowBytes := src.Bounds().Dx() * 4
	for y := 0; y < rows; y++ {
		sOff := (srcStartY + y) * src.Stride
		dOff := y * dstStride
		copy(dst[dOff:dOff+rowBytes], src.Pix[sOff:sOff+rowBytes])
	}
}

func absDiff8(a, b uint8) uint8 {
	if a >= b {
		return a - b
	}
	return b - a
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
