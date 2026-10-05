package svc

import (
	"testing"
	"time"

	"pieceomoney/internal/pkg/storage/repo"
)

var utc = time.UTC

func tx(merchant string, amount int64, ts time.Time) repo.Transaction {
	return repo.Transaction{Merchant: merchant, AmountMinor: amount, Currency: "KZT", TS: ts}
}

func TestGroupByMergesCaseAndSkipsOtherCurrencies(t *testing.T) {
	now := time.Now()
	list := []repo.Transaction{
		{Merchant: "Magnum", AmountMinor: 100, Currency: "KZT", TS: now},
		{Merchant: "MAGNUM", AmountMinor: 300, Currency: "KZT", TS: now},
		{Merchant: "Steam", AmountMinor: 999, Currency: "USD", TS: now},
		{Merchant: "Cafe", AmountMinor: 50, Currency: "KZT", TS: now},
	}
	got := ByMerchant(list, "KZT")
	if len(got) != 2 || got[0].Key != "Magnum" || got[0].AmountMinor != 400 || got[0].Count != 2 {
		t.Fatalf("unexpected groups: %+v", got)
	}
}

func TestTags(t *testing.T) {
	if got := NormalizeTags("#Trip, gift  work;trip"); got != "trip,gift,work" {
		t.Errorf("NormalizeTags = %q", got)
	}
	list := []repo.Transaction{
		{Merchant: "A", AmountMinor: 100, Currency: "KZT", Tags: "trip,gift"},
		{Merchant: "B", AmountMinor: 200, Currency: "KZT", Tags: "trip"},
		{Merchant: "C", AmountMinor: 50, Currency: "KZT"},
	}
	got := ByTag(list, "KZT")
	if len(got) != 2 || got[0].Key != "trip" || got[0].AmountMinor != 300 {
		t.Fatalf("unexpected tag groups: %+v", got)
	}
}

func TestSearch(t *testing.T) {
	list := []repo.Transaction{
		{Merchant: "Love Is Coffee", Tags: "work"},
		{Merchant: "Магнум", Category: "Food & Drinks", Note: "Продукты на неделю"},
		{Merchant: "Sulpak", Tags: "gift,trip"},
	}
	cases := map[string]int{"coffee": 1, "МАГНУМ": 1, "продукты": 1, "#trip": 1, "#coffee": 0, "food": 1, "": 0, "zzz": 0}
	for q, want := range cases {
		if got := len(Search(list, q)); got != want {
			t.Errorf("Search(%q) = %d results, want %d", q, got, want)
		}
	}
}

func TestDayAndMonthTotals(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, utc)
	list := []repo.Transaction{
		tx("a", 100, time.Date(2026, 10, 1, 9, 0, 0, 0, utc)),
		tx("a", 200, time.Date(2026, 10, 1, 21, 0, 0, 0, utc)),
		tx("a", 400, time.Date(2026, 10, 3, 12, 0, 0, 0, utc)),
		tx("a", 800, time.Date(2026, 9, 15, 12, 0, 0, 0, utc)),
	}
	days := DayTotals(list, "KZT", utc, start, 5)
	if days[0] != 300 || days[1] != 0 || days[2] != 400 {
		t.Errorf("DayTotals = %v", days)
	}
	months := MonthTotals(list, "KZT", utc, time.Date(2026, 10, 20, 0, 0, 0, 0, utc), 3)
	if len(months) != 3 || months[2].AmountMinor != 700 || months[1].AmountMinor != 800 || months[0].AmountMinor != 0 {
		t.Errorf("MonthTotals = %+v", months)
	}
}

func TestDetectRecurring(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, utc)
	var list []repo.Transaction
	// Netflix: monthly, slightly drifting dates, same amount.
	for _, d := range []time.Time{
		time.Date(2026, 7, 5, 10, 0, 0, 0, utc),
		time.Date(2026, 8, 5, 10, 0, 0, 0, utc),
		time.Date(2026, 9, 6, 10, 0, 0, 0, utc),
		time.Date(2026, 10, 5, 10, 0, 0, 0, utc),
	} {
		list = append(list, tx("Netflix", 350000, d))
	}
	// Coffee: frequent but irregular, varying amounts: not a subscription.
	for i, d := range []int{1, 2, 4, 9, 10, 15, 19} {
		list = append(list, tx("Coffee", int64(80000+i*30000), time.Date(2026, 10, d, 9, 0, 0, 0, utc)))
	}
	// Old gym: was monthly, stopped long ago: considered cancelled.
	for _, d := range []time.Time{
		time.Date(2025, 1, 10, 0, 0, 0, 0, utc), time.Date(2025, 2, 10, 0, 0, 0, 0, utc), time.Date(2025, 3, 10, 0, 0, 0, 0, utc),
	} {
		list = append(list, tx("Gym", 1500000, d))
	}

	got := DetectRecurring(list, now)
	if len(got) != 1 || got[0].Merchant != "Netflix" || got[0].Label != "monthly" || got[0].AmountMinor != 350000 {
		t.Fatalf("unexpected recurring: %+v", got)
	}
	if next := got[0].Next; next.Month() != time.November || next.Day() < 3 || next.Day() > 7 {
		t.Errorf("next payment = %v, want early November", next)
	}
}

func TestUnusualThreshold(t *testing.T) {
	if _, ok := UnusualThreshold([]int64{100, 200}, 3); ok {
		t.Error("too little history must not produce a threshold")
	}
	var amounts []int64
	for i := 0; i < 20; i++ {
		amounts = append(amounts, int64(10000+i*500)) // 100..195
	}
	thr, ok := UnusualThreshold(amounts, 3)
	if !ok {
		t.Fatal("expected a threshold with 20 payments of history")
	}
	if !(60000 > thr) {
		t.Errorf("a 600.00 payment against ~150.00 typical should exceed threshold %d", thr)
	}
	if 20000 > thr {
		t.Errorf("a 200.00 payment must not exceed threshold %d", thr)
	}
}
