package svc

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"pieceomoney/internal/core/cfg"
	"pieceomoney/internal/pkg/storage/repo"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := repo.Connect(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	return New(&cfg.Config{DefaultCurrency: "KZT", Location: time.UTC, OwnerUserID: 1},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo.NewTransactionsRepo(db), repo.NewBudgetsRepo(db), repo.NewRulesRepo(db))
}

func seed(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	items := []repo.Transaction{
		{TS: now, AmountMinor: 250000, Currency: "KZT", Merchant: "Magnum", Card: "Kaspi Gold", Category: "Food & Drinks"},
		{TS: now.Add(-time.Hour), AmountMinor: 90000, Currency: "KZT", Merchant: `ТОО "Малина"`, Category: "Food & Drinks"},
		{TS: now.AddDate(0, 0, -3), AmountMinor: 500000, Currency: "KZT", Merchant: "Yandex Go", Category: "Travel"},
		{TS: now.AddDate(0, -1, 0), AmountMinor: 700000, Currency: "KZT", Merchant: "Sulpak", Category: "Shopping"},
		{TS: now, AmountMinor: 1500, Currency: "USD", Merchant: "Steam", Category: "Entertainment"},
		{TS: now, AmountMinor: 30000, Currency: "KZT", Merchant: "Kiosk", Category: uncategorized},
	}
	for i, tx := range items {
		tx.Fingerprint = string(rune('a' + i))
		if _, _, err := s.txs.Insert(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.budgets.Set(ctx, "Food & Drinks", 400000); err != nil {
		t.Fatal(err)
	}
}

// Walks every button reachable from the main menu: each must route without
// error and fit Telegram's 64-byte callback data limit. Mutating routes are
// skipped here and covered separately.
func TestAllReachableScreens(t *testing.T) {
	s := newTestService(t)
	seed(t, s)
	ctx := context.Background()

	skip := func(data string) bool {
		for _, p := range []string{"ex", "tdy:", "bdy:", "tk:", "bp:"} {
			if data == p || strings.HasPrefix(data, p) {
				return true
			}
		}
		return false
	}

	seen := map[string]bool{}
	queue := []string{"m"}
	for len(queue) > 0 {
		data := queue[0]
		queue = queue[1:]
		if seen[data] || skip(data) {
			continue
		}
		seen[data] = true

		sc, err := s.route(ctx, data)
		if err != nil {
			t.Fatalf("route %q: %v", data, err)
		}
		if strings.TrimSpace(sc.text) == "" {
			t.Errorf("route %q: empty text", data)
		}
		for _, row := range sc.rows {
			for _, b := range row {
				if len(b.CallbackData) > 64 {
					t.Errorf("route %q: callback data %q is %d bytes", data, b.CallbackData, len(b.CallbackData))
				}
				queue = append(queue, b.CallbackData)
			}
		}
	}
	if len(seen) < 25 {
		t.Errorf("only %d screens reached, navigation looks broken", len(seen))
	}
}

func TestMutatingRoutes(t *testing.T) {
	s := newTestService(t)
	seed(t, s)
	ctx := context.Background()

	list, _ := s.txs.Last(ctx, 10)
	var kiosk repo.Transaction
	for _, tx := range list {
		if tx.Merchant == "Kiosk" {
			kiosk = tx
		}
	}

	// Recategorize: updates the payment and remembers the merchant.
	if _, err := s.route(ctx, "tk:"+itoa(kiosk.ID)+":"+catKey("Shopping")); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.txs.Get(ctx, kiosk.ID)
	if got.Category != "Shopping" {
		t.Errorf("category = %q", got.Category)
	}
	if c, _ := s.rules.For(ctx, "Kiosk"); c != "Shopping" {
		t.Errorf("merchant rule = %q", c)
	}

	// The rule is per merchant: other uncategorized payments of the same
	// merchant follow, differently categorized ones don't.
	for i, tx := range []repo.Transaction{
		{TS: time.Now(), AmountMinor: 100, Currency: "KZT", Merchant: "Cafe", Category: uncategorized},
		{TS: time.Now(), AmountMinor: 200, Currency: "KZT", Merchant: "cafe", Category: uncategorized},
		{TS: time.Now(), AmountMinor: 300, Currency: "KZT", Merchant: "Cafe", Category: "Health"},
		{TS: time.Now(), AmountMinor: 400, Currency: "KZT", Merchant: "Other", Category: uncategorized},
	} {
		tx.Fingerprint = "cafe" + itoa(int64(i))
		if _, _, err := s.txs.Insert(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.txs.Last(ctx, 50)
	var firstCafe int64
	for _, tx := range all {
		if tx.Merchant == "Cafe" && tx.AmountMinor == 100 {
			firstCafe = tx.ID
		}
	}
	if _, err := s.route(ctx, "tk:"+itoa(firstCafe)+":"+catKey("Shopping")); err != nil {
		t.Fatal(err)
	}
	byAmount := map[int64]string{}
	all, _ = s.txs.Last(ctx, 50)
	for _, tx := range all {
		byAmount[tx.AmountMinor] = tx.Category
	}
	if byAmount[100] != "Shopping" || byAmount[200] != "Shopping" || byAmount[300] != "Health" || byAmount[400] != uncategorized {
		t.Errorf("merchant categorization wrong: %v", byAmount)
	}

	// Preset sets a limit: 100k -> 10,000,000 minor.
	if _, err := s.route(ctx, "bp:"+catKey("Travel")+":100"); err != nil {
		t.Fatal(err)
	}
	if b, ok, _ := s.budgets.Get(ctx, "Travel"); !ok || b.LimitMinor != 10000000 {
		t.Errorf("travel budget = %+v ok=%v", b, ok)
	}

	// Delete limit and payment.
	if _, err := s.route(ctx, "bdy:"+catKey("Travel")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.budgets.Get(ctx, "Travel"); ok {
		t.Error("budget should be deleted")
	}
	if _, err := s.route(ctx, "tdy:"+itoa(kiosk.ID)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.txs.Get(ctx, kiosk.ID); ok {
		t.Error("payment should be deleted")
	}

	// A stale button pointing at a deleted payment falls back, not errors.
	if _, err := s.route(ctx, "t:"+itoa(kiosk.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestTypedLimitAmountIsPending(t *testing.T) {
	s := newTestService(t)
	if _, err := s.route(context.Background(), "bs:"+catKey("Travel")); err != nil {
		t.Fatal(err)
	}
	if got := s.takePending(); got != "Travel" {
		t.Errorf("pending = %q, want Travel", got)
	}
	if got := s.takePending(); got != "" {
		t.Errorf("pending should be consumed, got %q", got)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
