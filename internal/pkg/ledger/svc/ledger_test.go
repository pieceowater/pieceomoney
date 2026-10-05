package svc

import (
	"strings"
	"testing"
	"time"
)

func TestParseAmount(t *testing.T) {
	cases := map[string]int64{
		"2500":       250000,
		"2500.00":    250000,
		"2500.5":     250050,
		"2 500,00":   250000,
		"2 500,00 ₸": 250000,
		"1,234.56":   123456,
		"1.234,56":   123456,
		"1,234":      123400,
		"-300":       -30000,
		"0.99":       99,
	}
	for in, want := range cases {
		got, err := parseAmount(in)
		if err != nil || got != want {
			t.Errorf("parseAmount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseAmount("abc"); err == nil {
		t.Error("parseAmount(abc) should fail")
	}
}

func TestParse(t *testing.T) {
	loc := time.FixedZone("ALM", 5*3600)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	tx, err := Parse("```json\n{\"amount\": 2500.00, \"currency\":\"kzt\", \"merchant\":\"Magnum\", \"card\":\"Halyk Gold\", \"date\":\"2026-10-05T16:00:00Z\"}\n```", "KZT", loc, now)
	if err != nil {
		t.Fatal(err)
	}
	if tx.AmountMinor != 250000 || tx.Currency != "KZT" || tx.Merchant != "Magnum" {
		t.Fatalf("unexpected tx: %+v", tx)
	}
	got := FormatSaved(tx, loc)
	for _, want := range []string{"2,500 KZT", "Magnum", "Halyk Gold", "05.10.2026 21:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatSaved missing %q in:\n%s", want, got)
		}
	}

	again, _ := Parse(`{"amount":"2500","merchant":"Magnum","card":"Halyk Gold","date":"2026-10-05T16:00:00Z"}`, "KZT", loc, now)
	if again.Fingerprint != tx.Fingerprint {
		t.Error("same payment in a different shape should share a fingerprint")
	}

	for _, bad := range []string{"привет", "{oops", `{"merchant":"x"}`, `{"amount":"abc"}`, `{"amount":1,"date":"вчера"}`} {
		if _, err := Parse(bad, "KZT", loc, now); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestParsePipe(t *testing.T) {
	loc := time.FixedZone("ALM", 5*3600)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	tx, err := Parse(`₸2,500.00|ТОО "Магнум"|Kaspi Gold|Food & Drinks`, "KZT", loc, now)
	if err != nil {
		t.Fatal(err)
	}
	if tx.AmountMinor != 250000 || tx.Merchant != `ТОО "Магнум"` || tx.Card != "Kaspi Gold" || tx.Category != "Food & Drinks" || tx.Currency != "KZT" {
		t.Fatalf("unexpected tx: %+v", tx)
	}

	// Two firings seconds apart (no date in the payload) must dedupe.
	again, _ := Parse(`₸2,500.00|ТОО "Магнум"|Kaspi Gold|Food & Drinks`, "KZT", loc, now.Add(20*time.Second))
	if again.Fingerprint != tx.Fingerprint {
		t.Error("dateless duplicates within a minute should share a fingerprint")
	}

	if _, err := Parse("привет", "KZT", loc, now); err == nil {
		t.Error("plain text should fail")
	}
}

func TestParseDetectsCurrencyFromAmount(t *testing.T) {
	loc := time.UTC
	now := time.Now()
	cases := map[string]string{
		"KZT 350.00|Love Is Coffee|Freedom Deposit Card|": "KZT",
		"USD 12.50|Steam|Kaspi Gold|":                     "USD",
		"€5,00|Bakery||":                                  "EUR",
		"₸2,500.00|Magnum||":                              "KZT",
		"350|Kiosk||":                                     "KZT", // falls back to the default
		"GBP 3|Shop|||USD":                                "USD", // explicit currency field wins
	}
	for in, want := range cases {
		tx, err := Parse(in, "KZT", loc, now)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if tx.Currency != want {
			t.Errorf("Parse(%q) currency = %s, want %s", in, tx.Currency, want)
		}
	}
	if tx, _ := Parse("KZT 350.00|Love Is Coffee|Freedom Deposit Card|", "KZT", loc, now); tx.AmountMinor != 35000 {
		t.Errorf("amount = %d, want 35000", tx.AmountMinor)
	}
}

func TestParseManual(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 5, 15, 30, 0, 0, time.UTC)

	tx, err := ParseManual("1500 Такси вчера #Работа #поездка", "KZT", loc, now)
	if err != nil {
		t.Fatal(err)
	}
	if tx.AmountMinor != 150000 || tx.Merchant != "Такси" || tx.Currency != "KZT" || tx.Tags != "работа,поездка" || tx.Card != "Вручную" {
		t.Fatalf("unexpected tx: %+v", tx)
	}
	if want := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC); !tx.TS.Equal(want) {
		t.Errorf("date = %v, want %v", tx.TS, want)
	}

	cases := []struct {
		in       string
		merchant string
		minor    int64
		currency string
		day      int
		month    time.Month
	}{
		{"2500.50 Кофе с собой", "Кофе с собой", 250050, "KZT", 5, 10},
		{"12 USD Steam", "Steam", 1200, "USD", 5, 10},
		{"$5 Kiosk", "Kiosk", 500, "USD", 5, 10},
		{"300 Булочная 03.10", "Булочная", 30000, "KZT", 3, 10},
		{"300 Булочная 05.12", "Булочная", 30000, "KZT", 5, 12}, // future month -> last year
		{"400 Bakery yesterday", "Bakery", 40000, "KZT", 4, 10},
		{"400 Bakery today", "Bakery", 40000, "KZT", 5, 10},
		{"-700 Возврат Sulpak", "Возврат Sulpak", -70000, "KZT", 5, 10},
	}
	for _, c := range cases {
		got, err := ParseManual(c.in, "KZT", loc, now)
		if err != nil {
			t.Errorf("ParseManual(%q): %v", c.in, err)
			continue
		}
		if got.Merchant != c.merchant || got.AmountMinor != c.minor || got.Currency != c.currency ||
			got.TS.Day() != c.day || got.TS.Month() != c.month {
			t.Errorf("ParseManual(%q) = %+v", c.in, got)
		}
	}
	if got, _ := ParseManual("300 Булочная 05.12", "KZT", loc, now); got.TS.Year() != 2025 {
		t.Errorf("05.12 typed in October should resolve to 2025, got %d", got.TS.Year())
	}

	// Same entry twice is not a duplicate.
	a, _ := ParseManual("100 Кофе", "KZT", loc, now)
	b, _ := ParseManual("100 Кофе", "KZT", loc, now.Add(time.Nanosecond))
	if a.Fingerprint == b.Fingerprint {
		t.Error("manual entries must never share a fingerprint")
	}

	for _, bad := range []string{"", "Такси 1500", "1500", "1500 вчера", "1500 #tag"} {
		if _, err := ParseManual(bad, "KZT", loc, now); err == nil {
			t.Errorf("ParseManual(%q) should fail", bad)
		}
	}
}
