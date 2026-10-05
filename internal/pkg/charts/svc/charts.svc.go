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

// Pie draws the slices on the left and a legend (colour swatch, name, share
// and amount) on the right instead of writing text on the pie itself.
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

	values := make([]chart.Value, len(slices))
	for i, s := range slices {
		values[i] = chart.Value{
			Value: float64(s.Amount),
			Style: chart.Style{FillColor: palette[i%len(palette)], StrokeColor: drawing.ColorWhite, StrokeWidth: 2},
		}
	}
	const pieSize = 560
	pie := chart.PieChart{Width: pieSize, Height: pieSize, Font: f, Values: values, ColorPalette: nil}
	var pieBuf bytes.Buffer
	if err := pie.Render(chart.PNG, &pieBuf); err != nil {
		return nil, err
	}
	pieImg, err := png.Decode(&pieBuf)
	if err != nil {
		return nil, err
	}

	const legendW, rowH = 560, 44
	height := max(pieSize, 40+len(slices)*rowH+40)
	canvas := image.NewRGBA(image.Rect(0, 0, pieSize+legendW, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(0, (height-pieSize)/2, pieSize, (height-pieSize)/2+pieSize), pieImg, image.Point{}, draw.Over)

	ctx := freetype.NewContext()
	ctx.SetDPI(72)
	ctx.SetFont(f)
	ctx.SetFontSize(24)
	ctx.SetClip(canvas.Bounds())
	ctx.SetDst(canvas)
	ctx.SetHinting(font.HintingFull)
	ctx.SetSrc(image.NewUniform(color.RGBA{0x22, 0x22, 0x22, 255}))

	top := (height - len(slices)*rowH) / 2
	for i, s := range slices {
		y := top + i*rowH
		c := palette[i%len(palette)]
		swatch := image.Rect(pieSize+10, y+8, pieSize+38, y+36)
		draw.Draw(canvas, swatch, image.NewUniform(color.RGBA{c.R, c.G, c.B, 255}), image.Point{}, draw.Src)

		line := fmt.Sprintf("%s  %d%%  %s", s.Label, int(s.Amount*100/total), format(s.Amount))
		if _, err := ctx.DrawString(truncate(line, 34), freetype.Pt(pieSize+52, y+32)); err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
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

// Bars draws a simple bar chart; amounts are shown in major units.
func Bars(bars []Bar, width, height int) ([]byte, error) {
	if len(bars) == 0 {
		return nil, fmt.Errorf("charts: nothing to draw")
	}
	f, err := loadFont()
	if err != nil {
		return nil, err
	}

	values := make([]chart.Value, len(bars))
	for i, b := range bars {
		values[i] = chart.Value{Label: b.Label, Value: float64(b.Amount) / 100}
	}
	c := chart.BarChart{
		Width: width, Height: height, Font: f,
		BarWidth:   max(8, width/len(bars)-6),
		BarSpacing: 6,
		Background: chart.Style{Padding: chart.Box{Top: 30, Left: 20, Right: 70, Bottom: 50}},
		YAxis:      chart.YAxis{Style: chart.Style{FontSize: 12}, Range: &chart.ContinuousRange{Min: 0, Max: niceMax(bars)}},
		XAxis:      chart.Style{FontSize: 12},
		Bars:       values,
	}
	// Single colour for every bar.
	for i := range c.Bars {
		c.Bars[i].Style = chart.Style{FillColor: palette[0], StrokeColor: palette[0]}
	}
	var buf bytes.Buffer
	if err := c.Render(chart.PNG, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// niceMax gives the Y axis headroom above the tallest bar, in major units.
func niceMax(bars []Bar) float64 {
	var top int64
	for _, b := range bars {
		if b.Amount > top {
			top = b.Amount
		}
	}
	if top <= 0 {
		return 1
	}
	return float64(top) / 100 * 1.1
}
