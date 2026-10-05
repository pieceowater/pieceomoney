// Package svc: the single default handler registered in internal/app.go.
// Payments arrive as stopped polls posted with the bot token (the iOS
// Shortcut) or as JSON typed by the owner in the private chat. Messages from
// anyone else are dropped without a reply so strangers can't tell the bot is
// alive.
//
// The UI is one message the owner navigates with inline buttons (screens.go);
// callbacks edit it in place instead of spamming the chat.
package svc

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"pieceomoney/internal/core/cfg"
	ledgersvc "pieceomoney/internal/pkg/ledger/svc"
	"pieceomoney/internal/pkg/storage/repo"
)

const uncategorized = "Без категории"

// Budget alert thresholds, as a fraction of the monthly limit.
const (
	warnAt = 0.9
	stopAt = 1.0
)

type Service struct {
	cfg     *cfg.Config
	logger  *slog.Logger
	txs     *repo.TransactionsRepo
	budgets *repo.BudgetsRepo
	rules   *repo.RulesRepo

	// pendingBudget is the category whose limit amount the owner's next
	// plain-text message sets ("" = none). Single owner, so one slot.
	mu            sync.Mutex
	pendingBudget string
}

func New(c *cfg.Config, logger *slog.Logger, txs *repo.TransactionsRepo, budgets *repo.BudgetsRepo, rules *repo.RulesRepo) *Service {
	return &Service{cfg: c, logger: logger, txs: txs, budgets: budgets, rules: rules}
}

func (s *Service) setPending(category string) {
	s.mu.Lock()
	s.pendingBudget = category
	s.mu.Unlock()
}

func (s *Service) takePending() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.pendingBudget
	s.pendingBudget = ""
	return c
}

func (s *Service) HandleUpdate(ctx context.Context, b *tgbot.Bot, u *models.Update) {
	if u.CallbackQuery != nil {
		s.handleCallback(ctx, b, u.CallbackQuery)
		return
	}

	// A bot never receives updates for its own messages, but it does receive
	// the final state of polls it sent and stopped. The Shortcut uses that as
	// a one-bot transport: sendPoll (payload JSON as the question), stopPoll.
	// Only the bot token can create such a poll, so no sender check is needed.
	if u.Poll != nil {
		if u.Poll.IsClosed {
			s.ingest(ctx, b, u.Poll.Question)
		}
		return
	}

	m := u.Message
	if m == nil || m.From == nil || m.Text == "" {
		return
	}

	if m.Chat.Type != models.ChatTypePrivate || m.From.ID != s.cfg.OwnerUserID {
		s.logger.Warn("dropped message from unauthorized sender",
			slog.Int64("chat_id", m.Chat.ID), slog.Int64("user_id", m.From.ID))
		return
	}

	if s.handleOwnerText(ctx, b, m.Text) {
		return
	}
	s.ingest(ctx, b, m.Text)
}

// send posts a new message to the owner.
func (s *Service) send(ctx context.Context, b *tgbot.Bot, text string, rows [][]models.InlineKeyboardButton) {
	p := &tgbot.SendMessageParams{ChatID: s.cfg.OwnerUserID, Text: text, ParseMode: models.ParseModeHTML}
	if len(rows) > 0 {
		p.ReplyMarkup = &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	if _, err := b.SendMessage(ctx, p); err != nil {
		s.logger.Error("failed to send message", slog.Any("error", err))
	}
}

func (s *Service) ingest(ctx context.Context, b *tgbot.Bot, text string) {
	t, err := ledgersvc.Parse(text, s.cfg.DefaultCurrency, s.cfg.Location, time.Now())
	if err != nil {
		s.logger.Warn("failed to parse transaction", slog.Any("error", err))
		shown := text
		if r := []rune(shown); len(r) > 300 {
			shown = string(r[:300]) + "…"
		}
		s.send(ctx, b, "⚠️ <b>Не удалось разобрать транзакцию:</b> "+html.EscapeString(err.Error())+
			"\n\nПришло:\n<code>"+html.EscapeString(shown)+"</code>", menuRow())
		return
	}

	// Category precedence: what the payload says, then a remembered
	// merchant rule, then a visible placeholder.
	if t.Category == "" && t.Merchant != "" {
		if t.Category, err = s.rules.For(ctx, t.Merchant); err != nil {
			s.logger.Error("merchant rule lookup failed", slog.Any("error", err))
		}
	}
	if t.Category == "" {
		t.Category = uncategorized
	}

	id, inserted, err := s.txs.Insert(ctx, t)
	if err != nil {
		s.logger.Error("failed to store transaction", slog.Any("error", err))
		s.send(ctx, b, "❌ Не смог сохранить транзакцию, см. логи.", nil)
		return
	}
	if !inserted {
		s.logger.Info("duplicate transaction ignored")
		s.send(ctx, b, "♻️ <b>Дубликат</b>, уже записано:\n\n"+ledgersvc.FormatSaved(t, s.cfg.Location), menuRow())
		return
	}
	t.ID = id
	s.logger.Info("transaction stored",
		slog.Int64("amount_minor", t.AmountMinor), slog.String("currency", t.Currency),
		slog.String("merchant", t.Merchant), slog.String("category", t.Category))

	s.send(ctx, b, ledgersvc.FormatSaved(t, s.cfg.Location), [][]models.InlineKeyboardButton{
		{btn("🏷 Категория", fmt.Sprintf("tc:%d", id)), btn("🗑 Удалить", fmt.Sprintf("td:%d", id))},
		{btn("📊 Обзор", "ov:m"), btn("🏠 Меню", "m")},
	})

	if text, rows := s.budgetAlert(ctx, t); text != "" {
		s.send(ctx, b, text, rows)
	}
}

// budgetAlert returns a warning when t pushed its category's month spend
// across 90% or 100% of the limit. Stateless: it compares spend before and
// after this payment, so each threshold fires exactly once as it's crossed.
func (s *Service) budgetAlert(ctx context.Context, t repo.Transaction) (string, [][]models.InlineKeyboardButton) {
	if t.Currency != s.cfg.DefaultCurrency || t.AmountMinor <= 0 {
		return "", nil
	}
	monthStart := s.monthStart(time.Now())
	if t.TS.Before(monthStart) {
		return "", nil
	}

	budget, ok, err := s.budgets.Get(ctx, t.Category)
	if err != nil {
		s.logger.Error("budget lookup failed", slog.Any("error", err))
		return "", nil
	}
	if !ok || budget.LimitMinor <= 0 {
		return "", nil
	}
	after, err := s.txs.CategorySpentSince(ctx, t.Category, monthStart, t.Currency)
	if err != nil {
		s.logger.Error("category spend lookup failed", slog.Any("error", err))
		return "", nil
	}
	before := after - t.AmountMinor

	crossed := func(frac float64) bool {
		line := int64(float64(budget.LimitMinor) * frac)
		return before < line && after >= line
	}
	status := fmt.Sprintf("%s из %s (%d%%)\n%s", s.money(after), s.money(budget.LimitMinor),
		pct(after, budget.LimitMinor), bar(after, budget.LimitMinor))
	name := catEmoji(budget.Category) + " " + html.EscapeString(budget.Category)
	rows := [][]models.InlineKeyboardButton{{btn("🎯 Лимит", "b:"+catKey(budget.Category)), btn("🏠 Меню", "m")}}

	switch {
	case crossed(stopAt):
		return fmt.Sprintf("🚫 <b>Лимит превышен: %s</b>\n\n%s\n\nСтоп.", name, status), rows
	case crossed(warnAt):
		return fmt.Sprintf("⚠️ <b>90%% лимита: %s</b>\n\n%s\n\nПора притормозить.", name, status), rows
	}
	return "", nil
}

func (s *Service) monthStart(now time.Time) time.Time {
	n := now.In(s.cfg.Location)
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, s.cfg.Location)
}

func (s *Service) money(minor int64) string {
	return ledgersvc.FormatAmount(minor) + " " + html.EscapeString(s.cfg.DefaultCurrency)
}

func pct(part, whole int64) int {
	if whole <= 0 {
		return 0
	}
	return int(part * 100 / whole)
}

func bar(part, whole int64) string {
	const width = 10
	n := 0
	if whole > 0 && part > 0 {
		n = int(part * width / whole)
	}
	n = max(0, min(width, n))
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}
