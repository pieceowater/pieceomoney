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
