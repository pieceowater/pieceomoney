package svc

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func money(v int64) string { return itoa(v/100) + " KZT" }

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func TestPieAndBars(t *testing.T) {
	slices := []Slice{
		{"Food & Drinks", 420000}, {"Транспорт", 300000}, {"Покупки", 200000}, {"Health", 90000},
		{"Services", 70000}, {"Entertainment", 50000}, {"Travel", 40000}, {"Gifts", 30000}, {"Misc", 20000}, {"Tiny", 5000},
	}
	data, err := Pie(slices, money, "Другое")
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds().Dx() < 700 {
		t.Fatalf("pie png invalid: %v", err)
	}
	if p := os.Getenv("CHART_OUT"); p != "" {
		os.WriteFile(p+"/pie.png", data, 0o644)
	}

	bars := make([]Bar, 0, 31)
	for i := 1; i <= 31; i++ {
		bars = append(bars, Bar{Label: itoa(int64(i)), Amount: int64(10000 + (i*7919)%90000)})
	}
	data, err = Bars(bars, 780, 520, 15)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if p := os.Getenv("CHART_OUT"); p != "" {
		os.WriteFile(p+"/bars.png", data, 0o644)
	}

	trend := []Bar{{"май", 31200000}, {"июн", 28900000}, {"июл", 45000000}, {"авг", 38000000}, {"сен", 41500000}, {"окт", 12300000}}
	data, err = Bars(trend, 780, 520, 20)
	if err != nil {
		t.Fatal(err)
	}
	if p := os.Getenv("CHART_OUT"); p != "" {
		os.WriteFile(p+"/trend.png", data, 0o644)
	}

	// One slice must still draw a disc, not an empty canvas.
	one, err := Pie([]Slice{{"Food & Drinks", 35000}}, money, "x")
	if err != nil {
		t.Fatal(err)
	}
	img, _ = png.Decode(bytes.NewReader(one))
	if r, g, b, _ := img.At(img.Bounds().Dx()/2, 280).RGBA(); r>>8 == 255 && g>>8 == 255 && b>>8 == 255 {
		t.Error("single-slice pie is blank at its centre")
	}
	if p := os.Getenv("CHART_OUT"); p != "" {
		os.WriteFile(p+"/pie1.png", one, 0o644)
	}

	if _, err := Pie(nil, money, "x"); err == nil {
		t.Error("empty pie should error")
	}
}
