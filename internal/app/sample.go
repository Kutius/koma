package app

import (
	"image"
	"image/color"

	"github.com/egoist/mygo/ui"

	"koma/internal/canvas"
)

// GenerateSampleCanvas builds a crisp, realistic high-contrast dashboard UI card
// so the user can immediately explore Koma's Pixel Ruler, Pixelate,
// Arrows, Step Counters, and Pin windows right out of the box.
func GenerateSampleCanvas() *image.RGBA {
	const w, h = 860, 520
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="860" height="520" viewBox="0 0 860 520">
  <rect width="860" height="520" fill="#0F172A"/>
  <!-- Sidebar -->
  <rect x="0" y="0" width="210" height="520" fill="#111C33"/>
  <line x1="210" y1="0" x2="210" y2="520" stroke="#1E293B" stroke-width="1"/>
  <rect x="20" y="24" width="32" height="32" rx="9" fill="#0EA5E9"/>
  <circle cx="36" cy="40" r="7" fill="none" stroke="#FFFFFF" stroke-width="2.2"/>
  <text x="64" y="45" fill="#F8FAFC" font-family="Segoe UI, sans-serif" font-size="16" font-weight="700">Metrics OS</text>

  <rect x="16" y="84" width="178" height="36" rx="8" fill="#1E293B"/>
  <text x="34" y="107" fill="#38BDF8" font-family="Segoe UI, sans-serif" font-size="13" font-weight="600">Overview</text>
  <text x="34" y="148" fill="#94A3B8" font-family="Segoe UI, sans-serif" font-size="13">Deployments</text>
  <text x="34" y="186" fill="#94A3B8" font-family="Segoe UI, sans-serif" font-size="13">Edge Latency</text>
  <text x="34" y="224" fill="#94A3B8" font-family="Segoe UI, sans-serif" font-size="13">API Keys</text>

  <!-- Header -->
  <text x="242" y="48" fill="#F8FAFC" font-family="Segoe UI, sans-serif" font-size="22" font-weight="700">Production Telemetry</text>
  <rect x="698" y="24" width="130" height="34" rx="8" fill="#0EA5E9"/>
  <text x="763" y="46" fill="#FFFFFF" font-family="Segoe UI, sans-serif" font-size="13" font-weight="700" text-anchor="middle">Deploy Now</text>

  <!-- 3 KPI Cards -->
  <rect x="242" y="84" width="184" height="104" rx="12" fill="#1E293B" stroke="#334155" stroke-width="1"/>
  <text x="260" y="112" fill="#94A3B8" font-family="Segoe UI, sans-serif" font-size="12">P99 LATENCY</text>
  <text x="260" y="148" fill="#F8FAFC" font-family="Segoe UI, sans-serif" font-size="28" font-weight="700">14.2 ms</text>
  <text x="260" y="172" fill="#10B981" font-family="Segoe UI, sans-serif" font-size="12" font-weight="600">-18.4% vs last week</text>

  <rect x="444" y="84" width="184" height="104" rx="12" fill="#1E293B" stroke="#334155" stroke-width="1"/>
  <text x="462" y="112" fill="#94A3B8" font-family="Segoe UI, sans-serif" font-size="12">THROUGHPUT</text>
  <text x="462" y="148" fill="#F8FAFC" font-family="Segoe UI, sans-serif" font-size="28" font-weight="700">48.9k/s</text>
  <text x="462" y="172" fill="#38BDF8" font-family="Segoe UI, sans-serif" font-size="12" font-weight="600">99.99% uptime</text>

  <rect x="646" y="84" width="182" height="104" rx="12" fill="#1E293B" stroke="#334155" stroke-width="1"/>
  <text x="664" y="112" fill="#94A3B8" font-family="Segoe UI, sans-serif" font-size="12">API SECRET</text>
  <text x="664" y="146" fill="#F59E0B" font-family="Consolas, monospace" font-size="15" font-weight="700">sk_live_94f2a8c1</text>
  <text x="664" y="172" fill="#94A3B8" font-family="Segoe UI, sans-serif" font-size="11.5">Try Pixelate (X) here!</text>

  <!-- Main Chart Card -->
  <rect x="242" y="208" width="586" height="280" rx="12" fill="#1E293B" stroke="#334155" stroke-width="1"/>
  <text x="264" y="240" fill="#F8FAFC" font-family="Segoe UI, sans-serif" font-size="15" font-weight="600">Frame Render Time (D3D11 Native GPU)</text>
  <line x1="264" y1="274" x2="804" y2="274" stroke="#334155" stroke-width="1"/>
  <line x1="264" y1="330" x2="804" y2="330" stroke="#334155" stroke-width="1"/>
  <line x1="264" y1="386" x2="804" y2="386" stroke="#334155" stroke-width="1"/>
  <line x1="264" y1="442" x2="804" y2="442" stroke="#334155" stroke-width="1"/>

  <!-- Smooth Chart Bars -->
  <rect x="282" y="346" width="38" height="96" rx="6" fill="#0EA5E9"/>
  <rect x="342" y="314" width="38" height="128" rx="6" fill="#0EA5E9"/>
  <rect x="402" y="362" width="38" height="80" rx="6" fill="#38BDF8"/>
  <rect x="462" y="290" width="38" height="152" rx="6" fill="#6366F1"/>
  <rect x="522" y="328" width="38" height="114" rx="6" fill="#0EA5E9"/>
  <rect x="582" y="268" width="38" height="174" rx="6" fill="#10B981"/>
  <rect x="642" y="308" width="38" height="134" rx="6" fill="#0EA5E9"/>
  <rect x="702" y="284" width="38" height="158" rx="6" fill="#38BDF8"/>
  <rect x="762" y="336" width="38" height="106" rx="6" fill="#6366F1"/>
</svg>`

	parsed := ui.MustParseSVG([]byte(svg))
	img := parsed.Image(w, h, color.White)
	// Ensure opaque alpha
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

// AddDemoAnnotations adds a clean set of sample annotations (arrow, step counters,
// pixelate redaction, pixel ruler callout, and badge text) for testing & previews.
func (e *EditorState) AddDemoAnnotations() {
	if e.baseImg == nil {
		return
	}
	e.annotations = []canvas.Annotation{
		{
			ID:        1,
			Kind:      canvas.ToolPixelate,
			Start:     canvas.PointF{X: 660, Y: 126},
			End:       canvas.PointF{X: 814, Y: 158},
			BlockSize: 10,
		},
		{
			ID:          2,
			Kind:        canvas.ToolArrow,
			Start:       canvas.PointF{X: 615, Y: 44},
			End:         canvas.PointF{X: 692, Y: 41},
			Color:       ui.Hex("#EF4444"),
			StrokeWidth: 4,
		},
		{
			ID:         3,
			Kind:       canvas.ToolCounter,
			Start:      canvas.PointF{X: 256, Y: 96},
			Color:      ui.Hex("#0EA5E9"),
			CounterNum: 1,
		},
		{
			ID:         4,
			Kind:       canvas.ToolCounter,
			Start:      canvas.PointF{X: 658, Y: 96},
			Color:      ui.Hex("#F59E0B"),
			CounterNum: 2,
		},
		{
			ID:         5,
			Kind:       canvas.ToolMeasure,
			Start:      canvas.PointF{X: 540, Y: 380},
			Color:      ui.Hex("#10B981"),
			MeasLeft:   522,
			MeasRight:  560,
			MeasTop:    328,
			MeasBottom: 442,
		},
		{
			ID:       6,
			Kind:     canvas.ToolText,
			Start:    canvas.PointF{X: 432, Y: 28},
			Color:    ui.Hex("#EF4444"),
			Text:     "One-Click Deploy",
			FontSize: 16,
			Filled:   true,
		},
	}
	e.nextAnnID = 6
	e.nextCounter = 3
	e.rasterDirty = true
	e.rebuildCaches()
}
