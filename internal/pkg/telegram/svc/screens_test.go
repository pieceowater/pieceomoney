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
	return New(&cfg.Config{DefaultCurrency: "KZT", Location: time.UTC, OwnerUserID: 1, UnusualMultiplier: 3, DailyPaymentsWarn: 6},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo.NewTransactionsRepo(db), repo.NewBudgetsRepo(db), repo.NewRulesRepo(db), repo.NewGoalsRepo(db))
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
	// A monthly subscription so the recurring screen has something to show.
	for i := 0; i < 4; i++ {
		items = append(items, repo.Transaction{TS: now.AddDate(0, -i, -2), AmountMinor: 350000, Currency: "KZT",
			Merchant: "Netflix", Card: "Kaspi Gold", Category: "Entertainment"})
	}
	for i, tx := range items {
		tx.Fingerprint = "seed" + itoa(int64(i))
		id, _, err := s.txs.Insert(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		if tx.Merchant == "Magnum" {
			if err := s.txs.SetNote(ctx, id, "weekly groceries"); err != nil {
				t.Fatal(err)
			}
			if err := s.txs.SetTags(ctx, id, "home,family"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.budgets.Set(ctx, "Food & Drinks", 400000); err != nil {
		t.Fatal(err)
	}
	gid, err := s.goals.Create(ctx, "Vacation", 150000000, 15000000)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.goals.Deposit(ctx, gid, 5000000); err != nil {
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
		for _, p := range []string{"ex", "pic:", "tdy:", "bdy:", "tk:", "bp:", "gq:", "gdy:"} {
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
	for _, want := range []string{"cd:m", "sb", "sh:m", "tg:m", "ch", "fd", "gl", "g:1", "gp:1", "ge:1", "gn", "tn:1", "tt:1"} {
		if !seen[want] {
			t.Errorf("screen %q is not reachable from the main menu", want)
		}
	}
	if len(seen) < 40 {
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

func TestPromptsSetPending(t *testing.T) {
	s := newTestService(t)
	seed(t, s)
	ctx := context.Background()

	cases := []struct{ route, kind, arg string }{
		{"bs:" + catKey("Travel"), pendBudget, "Travel"},
		{"fd", pendFind, ""},
		{"tn:1", pendNote, "1"},
		{"tt:1", pendTags, "1"},
		{"gn", pendGoalNew, ""},
		{"ge:1", pendGoalEdit, "1"},
		{"gp:1", pendGoalDep, "1"},
	}
	for _, c := range cases {
		if _, err := s.route(ctx, c.route); err != nil {
			t.Fatalf("route %q: %v", c.route, err)
		}
		got := s.takePending()
		if got.kind != c.kind || got.arg != c.arg {
			t.Errorf("route %q pending = %+v, want {%s %s}", c.route, got, c.kind, c.arg)
		}
		if again := s.takePending(); again.kind != "" {
			t.Errorf("pending should be consumed, got %+v", again)
		}
	}
}

func TestGoalFlows(t *testing.T) {
	s := newTestService(t)
	seed(t, s)
	ctx := context.Background()

	name, target, monthly, err := parseGoal("Отпуск; 1 500 000; 150000")
	if err != nil || name != "Отпуск" || target != 150000000 || monthly != 15000000 {
		t.Fatalf("parseGoal = %q %d %d %v", name, target, monthly, err)
	}
	if _, _, _, err := parseGoal("только название"); err == nil {
		t.Error("goal without a target must fail")
	}

	// Quick deposit of the monthly amount, then a withdrawal.
	if _, err := s.route(ctx, "gq:1:m"); err != nil {
		t.Fatal(err)
	}
	if saved, _ := s.goals.SavedSince(ctx, 1, time.Time{}); saved != 5000000+15000000 {
		t.Errorf("saved = %d", saved)
	}
	if err := s.goals.Deposit(ctx, 1, -2000000); err != nil {
		t.Fatal(err)
	}
	sc, err := s.route(ctx, "g:1")
	if err != nil || !strings.Contains(sc.text, "Vacation") {
		t.Fatalf("goal screen: %v %q", err, sc.text)
	}

	if _, err := s.route(ctx, "gdy:1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.goals.Get(ctx, 1); ok {
		t.Error("goal should be deleted")
	}
	if saved, _ := s.goals.SavedSince(ctx, 1, time.Time{}); saved != 0 {
		t.Errorf("deposits should go with the goal, saved = %d", saved)
	}
}

func TestChartsRender(t *testing.T) {
	s := newTestService(t)
	seed(t, s)
	ctx := context.Background()
	for _, spec := range []string{"pie:m", "pie:pm", "pie:w", "days", "trend"} {
		data, caption, err := s.renderChart(ctx, spec)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		if len(data) < 1000 || caption == "" {
			t.Errorf("%s: no image (%d bytes) or caption %q", spec, len(data), caption)
		}
	}
}

func TestSubscriptionsScreenFindsNetflix(t *testing.T) {
	s := newTestService(t)
	seed(t, s)
	sc, err := s.route(context.Background(), "sb")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sc.text, "Netflix") {
		t.Errorf("recurring screen misses Netflix:\n%s", sc.text)
	}
}

func TestUnusualAndNewMerchantAlerts(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	now := time.Now()

	var last repo.Transaction
	for i := 0; i < 20; i++ {
		last = repo.Transaction{TS: now.Add(-time.Duration(i+1) * time.Hour), AmountMinor: 100000 + int64(i)*1000,
			Currency: "KZT", Merchant: "Cafe", Fingerprint: "h" + itoa(int64(i))}
		if _, _, err := s.txs.Insert(ctx, last); err != nil {
			t.Fatal(err)
		}
	}
	big := repo.Transaction{TS: now, AmountMinor: 5000000, Currency: "KZT", Merchant: "Jeweller", Fingerprint: "big"}
	id, _, err := s.txs.Insert(ctx, big)
	if err != nil {
		t.Fatal(err)
	}
	big.ID = id

	if a := s.unusualAlert(ctx, big); !strings.Contains(a, "Необычно") {
		t.Errorf("a 50,000 payment against ~1,000 typical must alert, got %q", a)
	}
	if !s.isNewMerchant(ctx, big) {
		t.Error("first payment to a merchant should be flagged new")
	}
	last.ID = 1
	if s.isNewMerchant(ctx, last) {
		t.Error("a merchant with earlier payments is not new")
	}
	normal := repo.Transaction{TS: now, AmountMinor: 105000, Currency: "KZT", Merchant: "Cafe", Fingerprint: "normal"}
	nid, _, _ := s.txs.Insert(ctx, normal)
	normal.ID = nid
	if a := s.unusualAlert(ctx, normal); a != "" {
		t.Errorf("a typical payment must not alert, got %q", a)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestDailyAlertFiresOnceAtThreshold(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	now := time.Now()

	var alerts int
	for i := 1; i <= 8; i++ {
		tx := repo.Transaction{TS: now, AmountMinor: 100, Currency: "KZT", Merchant: "Shop", Fingerprint: "d" + itoa(int64(i))}
		id, _, err := s.txs.Insert(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		tx.ID = id
		if a := s.dailyAlert(ctx, tx); a != "" {
			alerts++
			if i != 6 {
				t.Errorf("alert fired on payment %d, want 6", i)
			}
		}
	}
	if alerts != 1 {
		t.Errorf("alerts = %d, want exactly 1", alerts)
	}
}
