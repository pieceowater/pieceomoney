package svc

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pieceomoney/internal/core/cfg"
	"pieceomoney/internal/pkg/storage/repo"
)

func TestBudgetAlertFiresOncePerThreshold(t *testing.T) {
	ctx := context.Background()
	db, err := repo.Connect(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	txs, budgets := repo.NewTransactionsRepo(db), repo.NewBudgetsRepo(db)
	s := New(&cfg.Config{DefaultCurrency: "KZT", Location: time.UTC}, slog.New(slog.NewTextHandler(io.Discard, nil)),
		txs, budgets, repo.NewRulesRepo(db))

	if err := budgets.Set(ctx, "Food & Drinks", 100000); err != nil { // 1000.00
		t.Fatal(err)
	}

	n := 0
	pay := func(minor int64) string {
		n++
		tx := repo.Transaction{TS: time.Now(), AmountMinor: minor, Currency: "KZT", Category: "food & drinks",
			Fingerprint: string(rune('a' + n))}
		if _, _, err := txs.Insert(ctx, tx); err != nil {
			t.Fatal(err)
		}
		text, _ := s.budgetAlert(ctx, tx)
		return text
	}

	if a := pay(50000); a != "" { // 50%
		t.Errorf("50%%: unexpected alert %q", a)
	}
	if a := pay(41000); !strings.Contains(a, "90%") { // 91%
		t.Errorf("91%%: want warning, got %q", a)
	}
	if a := pay(1000); a != "" { // 92%, already warned
		t.Errorf("92%%: warning must not repeat, got %q", a)
	}
	if a := pay(10000); !strings.Contains(a, "превышен") { // 102%
		t.Errorf("102%%: want stop alert, got %q", a)
	}
	if a := pay(500); a != "" {
		t.Errorf("after stop: no repeat expected, got %q", a)
	}
}
