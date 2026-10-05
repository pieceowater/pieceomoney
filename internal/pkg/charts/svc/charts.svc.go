// Package svc renders the charts the bot sends as PNG images: a category pie
// with a colour legend, daily-spending bars and a monthly trend.
package svc

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	"github.com/wcharczuk/go-chart/v2"
	"github.com/wcharczuk/go-chart/v2/drawing"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
)

// Slice is one pie segment; Amount is in minor units.
type Slice struct {
	Label  string
	Amount int64
}

type Bar struct {
	Label  string
	Amount int64
}

var palette = []drawing.Color{
	{R: 0x2F, G: 0x5D, B: 0x8A, A: 255}, {R: 0xE0, G: 0x8E, B: 0x3C, A: 255},
	{R: 0x3A, G: 0xA6, B: 0x8B, A: 255}, {R: 0xC4, G: 0x4F, B: 0x6B, A: 255},
	{R: 0x8A, G: 0x6F, B: 0xC9, A: 255}, {R: 0xD9, G: 0xB5, B: 0x3F, A: 255},
	{R: 0x5B, G: 0xA3, B: 0xD9, A: 255}, {R: 0x7D, G: 0x8B, B: 0x99, A: 255},
}

const maxSlices = 8

func loadFont() (*truetype.Font, error) { return truetype.Parse(goregular.TTF) }

// Pie draws the pie on top and a legend (colour swatch, name, share and
// amount) below it instead of writing text on the slices. The layout is
// portrait so Telegram shows it at near full size and the text stays legible.
// Slices beyond the first seven are folded into "Other".
func Pie(slices []Slice, format func(int64) string, other string) ([]byte, error) {
	if len(slices) == 0 {
		return nil, fmt.Errorf("charts: nothing to draw")
	}
	slices = foldTail(slices, other)

	var total int64
	for _, s := range slices {
		total += s.Amount
	}
	if total <= 0 {
		return nil, fmt.Errorf("charts: non-positive total")
	}

	f, err := loadFont()
	if err != nil {
		return nil, err
	}

	const width, pieSize, rowH = 760, 540, 50
	pieImg, err := renderPie(f, slices, pieSize)
	if err != nil {
		return nil, err
	}

	height := pieSize + 30 + len(slices)*rowH + 30
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	left := (width - pieSize) / 2
	draw.Draw(canvas, image.Rect(left, 10, left+pieSize, 10+pieSize), pieImg, image.Point{}, draw.Over)

	ctx := freetype.NewContext()
	ctx.SetDPI(72)
	ctx.SetFont(f)
	ctx.SetFontSize(28)
	ctx.SetClip(canvas.Bounds())
	ctx.SetDst(canvas)
	ctx.SetHinting(font.HintingFull)
	ctx.SetSrc(image.NewUniform(color.RGBA{0x22, 0x22, 0x22, 255}))

	top := pieSize + 30
	for i, s := range slices {
		y := top + i*rowH
		c := palette[i%len(palette)]
		swatch := image.Rect(30, y+8, 62, y+40)
		draw.Draw(canvas, swatch, image.NewUniform(color.RGBA{c.R, c.G, c.B, 255}), image.Point{}, draw.Src)

		line := fmt.Sprintf("%s  %d%%  %s", s.Label, int(s.Amount*100/total), format(s.Amount))
		if _, err := ctx.DrawString(truncate(line, 36), freetype.Pt(78, y+35)); err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// renderPie draws just the disc. go-chart draws nothing for a single slice,
// so that case is a plain filled circle.
func renderPie(f *truetype.Font, slices []Slice, size int) (image.Image, error) {
	if len(slices) == 1 {
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		c := palette[0]
		fill := color.RGBA{c.R, c.G, c.B, 255}
		r := float64(size)/2 - 2
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				dx, dy := float64(x)+0.5-float64(size)/2, float64(y)+0.5-float64(size)/2
				d := math.Sqrt(dx*dx + dy*dy)
				switch {
				case d <= r-0.5:
					img.SetRGBA(x, y, fill)
				case d <= r+0.5: // one-pixel antialiased edge
					a := uint8(255 * (r + 0.5 - d))
					img.SetRGBA(x, y, color.RGBA{fill.R * a / 255, fill.G * a / 255, fill.B * a / 255, a})
				}
			}
		}
		return img, nil
	}

	values := make([]chart.Value, len(slices))
	for i, s := range slices {
		values[i] = chart.Value{
			Value: float64(s.Amount),
			Style: chart.Style{FillColor: palette[i%len(palette)], StrokeColor: drawing.ColorWhite, StrokeWidth: 2},
		}
	}
	pie := chart.PieChart{Width: size, Height: size, Font: f, Values: values}
	var buf bytes.Buffer
	if err := pie.Render(chart.PNG, &buf); err != nil {
		return nil, err
	}
	return png.Decode(&buf)
}

func foldTail(slices []Slice, other string) []Slice {
	if len(slices) <= maxSlices {
		return slices
	}
	head := append([]Slice(nil), slices[:maxSlices-1]...)
	var rest int64
	for _, s := range slices[maxSlices-1:] {
		rest += s.Amount
	}
	return append(head, Slice{Label: other, Amount: rest})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Bars draws a bar chart with labelled axes; amounts are in minor units and
// shown in major units. It is drawn by hand because go-chart silently drops
// X labels that don't fit a bar slot, which loses most days of a month.
// Labels that would collide are thinned by a stride instead, and charts with
// few bars print the value above each bar. fontSize is in points; Telegram
// downscales wide images, so keep it generous.
func Bars(bars []Bar, width, height int, fontSize float64) ([]byte, error) {
	if len(bars) == 0 {
		return nil, fmt.Errorf("charts: nothing to draw")
	}
	f, err := loadFont()
	if err != nil {
		return nil, err
	}
	face := truetype.NewFace(f, &truetype.Options{Size: fontSize, DPI: 72})
	defer face.Close()

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	ctx := freetype.NewContext()
	ctx.SetDPI(72)
	ctx.SetFont(f)
	ctx.SetFontSize(fontSize)
	ctx.SetClip(canvas.Bounds())
	ctx.SetDst(canvas)
	ctx.SetHinting(font.HintingFull)
	ctx.SetSrc(image.NewUniform(color.RGBA{0x33, 0x33, 0x33, 255}))

	text := func(s string, x, y int) { _, _ = ctx.DrawString(s, freetype.Pt(x, y)) }
	textWidth := func(s string) int { return font.MeasureString(face, s).Round() }

	var top int64
	for _, b := range bars {
		top = max(top, b.Amount)
	}
	step := niceStep(float64(max(top, 100)) / 100 / 4)
	ticks := int(math.Ceil(float64(top)/100/step - 1e-9))
	ticks = max(ticks, 1)
	axisMax := float64(ticks) * step

	labelW := 0
	for i := 0; i <= ticks; i++ {
		labelW = max(labelW, textWidth(groupThousands(int64(float64(i)*step))))
	}
	valueRoom := 0
	if len(bars) <= 8 {
		valueRoom = int(fontSize) + 8
	}
	plot := image.Rect(labelW+28, 20+valueRoom, width-20, height-int(fontSize)-24)
	plotW, plotH := plot.Dx(), plot.Dy()

	grid := image.NewUniform(color.RGBA{0xE3, 0xE3, 0xE3, 255})
	for i := 0; i <= ticks; i++ {
		y := plot.Max.Y - int(float64(plotH)*float64(i)/float64(ticks))
		draw.Draw(canvas, image.Rect(plot.Min.X, y, plot.Max.X, y+1), grid, image.Point{}, draw.Src)
		label := groupThousands(int64(float64(i) * step))
		text(label, plot.Min.X-8-textWidth(label), y+int(fontSize)/3)
	}

	slot := float64(plotW) / float64(len(bars))
	barW := max(3, int(slot*0.7))
	maxLabel := 0
	for _, b := range bars {
		maxLabel = max(maxLabel, textWidth(b.Label))
	}
	stride := max(1, int(math.Ceil(float64(maxLabel+10)/slot)))

	c := palette[0]
	barColor := image.NewUniform(color.RGBA{c.R, c.G, c.B, 255})
	for i, b := range bars {
		cx := plot.Min.X + int(slot*float64(i)+slot/2)
		h := int(float64(plotH) * float64(b.Amount) / 100 / axisMax)
		if b.Amount > 0 {
			h = max(h, 2)
			draw.Draw(canvas, image.Rect(cx-barW/2, plot.Max.Y-h, cx-barW/2+barW, plot.Max.Y), barColor, image.Point{}, draw.Src)
			if valueRoom > 0 {
				v := groupThousands(b.Amount / 100)
				text(v, cx-textWidth(v)/2, plot.Max.Y-h-6)
			}
		}
		if i%stride == 0 {
			text(b.Label, cx-textWidth(b.Label)/2, plot.Max.Y+int(fontSize)+10)
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// niceStep rounds a raw axis step up to 1, 2, 5 or 10 times a power of ten.
func niceStep(raw float64) float64 {
	if raw <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	switch frac := raw / mag; {
	case frac <= 1:
		return mag
	case frac <= 2:
		return 2 * mag
	case frac <= 5:
		return 5 * mag
	}
	return 10 * mag
}

func groupThousands(v int64) string {
	s := fmt.Sprintf("%d", v)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
