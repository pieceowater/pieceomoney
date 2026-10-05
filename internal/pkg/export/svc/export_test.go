package svc

import (
	"bytes"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"pieceomoney/internal/pkg/storage/repo"
)

func TestBuildXLSX(t *testing.T) {
	loc := time.FixedZone("ALM", 5*3600)
	txs := []repo.Transaction{
		{TS: time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC), AmountMinor: 150050, Currency: "KZT", Merchant: "Magnum", Card: "Kaspi Gold", Category: "Food & Drinks"},
		{TS: time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC), AmountMinor: 250000, Currency: "KZT", Merchant: "Yandex Go", Category: "Travel"},
		{TS: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), AmountMinor: 1000, Currency: "USD", Merchant: "Steam", Category: "Entertainment"},
	}
	data, err := BuildXLSX(txs, "KZT", loc)
	if err != nil {
		t.Fatal(err)
	}

	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.GetRows(sheetTx)
	if err != nil || len(rows) != 4 {
		t.Fatalf("transactions sheet: rows=%d err=%v", len(rows), err)
	}
	if rows[1][4] != "Magnum" {
		t.Errorf("merchant cell = %q", rows[1][4])
	}
	// 20:00 UTC on Sep 30 is already Oct 1 in UTC+5.
	if got, _ := f.GetCellValue(sheetSummary, "B1"); got != "2026-10" {
		t.Errorf("first month column = %q, want 2026-10", got)
	}
	if got, _ := f.GetCellValue(sheetSummary, "A4"); got != "Итого" {
		t.Errorf("total row label = %q", got)
	}
	if v, _ := f.GetCellValue(sheetSummary, "C4"); v != "4,000.50" && v != "4000.5" {
		t.Errorf("grand total = %q", v)
	}
}

func TestBuildXLSXEmpty(t *testing.T) {
	if _, err := BuildXLSX(nil, "KZT", time.UTC); err != nil {
		t.Fatal(err)
	}
}
