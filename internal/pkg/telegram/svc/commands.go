package svc

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	analyticssvc "pieceomoney/internal/pkg/analytics/svc"
	exportsvc "pieceomoney/internal/pkg/export/svc"
	ledgersvc "pieceomoney/internal/pkg/ledger/svc"
)

// Labels of the persistent bottom keyboard; pressing one arrives as a plain
// text message, so they're matched here before anything is parsed as JSON.
const (
	kbOverview = "📊 Обзор"
	kbBudgets  = "🎯 Лимиты"
	kbLast     = "🧾 Последние"
	kbMenu     = "🏠 Меню"
)

func bottomKeyboard() *models.ReplyKeyboardMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard: [][]models.KeyboardButton{
			{{Text: kbOverview}, {Text: kbBudgets}},
			{{Text: kbLast}, {Text: kbMenu}},
		},
		ResizeKeyboard: true,
		IsPersistent:   true,
	}
}

// handleOwnerText handles everything the owner can type that isn't a
// payment payload. Returns false to let the text fall through to ingest.
func (s *Service) handleOwnerText(ctx context.Context, b *tgbot.Bot, text string) bool {
	text = strings.TrimSpace(text)

	// Bottom-keyboard buttons navigate and cancel any pending prompt.
	routes := map[string]string{kbOverview: "ov:m", kbBudgets: "bl", kbLast: "ls", kbMenu: "m"}
	if route, ok := routes[text]; ok {
		s.setPending("", "")
		s.sendRoute(ctx, b, route)
		return true
	}

	if p := s.takePending(); p.kind != "" && !strings.HasPrefix(text, "/") {
		s.handlePending(ctx, b, p, text)
		return true
	}

	if !strings.HasPrefix(text, "/") {
		return false
	}
	s.handleCommand(ctx, b, text)
	return true
}

// handlePending applies the owner's free-text answer to the prompt that was
// open. A bad answer re-opens the same prompt with a hint.
func (s *Service) handlePending(ctx context.Context, b *tgbot.Bot, p pending, text string) {
	retry := func(msg, cancelRoute string) {
		s.setPending(p.kind, p.arg)
		s.send(ctx, b, msg, buttons{{btn("✖️ Отмена", cancelRoute)}})
	}
	fail := func(what string, err error) {
		s.logger.Error(what+" failed", slog.Any("error", err))
		s.send(ctx, b, "❌ Ошибка, см. логи.", menuRow())
	}
	id, _ := strconv.ParseInt(p.arg, 10, 64)

	switch p.kind {
	case pendBudget:
		limit, err := ledgersvc.ParseAmount(text)
		if err != nil || limit <= 0 {
			retry("Не понял сумму. Пришли число, например <code>150000</code>.", "b:"+catKey(p.arg))
			return
		}
		if err := s.budgets.Set(ctx, p.arg, limit); err != nil {
			fail("set budget", err)
			return
		}
		s.sendRoute(ctx, b, "b:"+catKey(p.arg))

	case pendFind:
		s.sendFind(ctx, b, text)

	case pendAdd:
		s.addManual(ctx, b, text)

	case pendNote:
		note := text
		if note == "-" {
			note = ""
		}
		if err := s.txs.SetNote(ctx, id, note); err != nil {
			fail("set note", err)
			return
		}
		s.sendRoute(ctx, b, "t:"+p.arg)

	case pendTags:
		tags := analyticssvc.NormalizeTags(text)
		if text == "-" {
			tags = ""
		}
		if err := s.txs.SetTags(ctx, id, tags); err != nil {
			fail("set tags", err)
			return
		}
		s.sendRoute(ctx, b, "t:"+p.arg)

	case pendGoalNew, pendGoalEdit:
		name, target, monthly, err := parseGoal(text)
		cancel := "gl"
		if p.kind == pendGoalEdit {
			cancel = "g:" + p.arg
		}
		if err != nil {
			retry(err.Error(), cancel)
			return
		}
		if p.kind == pendGoalNew {
			newID, err := s.goals.Create(ctx, name, target, monthly)
			if err != nil {
				fail("create goal", err)
				return
			}
			s.sendRoute(ctx, b, fmt.Sprintf("g:%d", newID))
			return
		}
		if err := s.goals.Update(ctx, id, name, target, monthly); err != nil {
			fail("update goal", err)
			return
		}
		s.sendRoute(ctx, b, "g:"+p.arg)

	case pendGoalDep:
		amount, err := ledgersvc.ParseAmount(text)
		if err != nil || amount == 0 {
			retry("Не понял сумму. Пришли число, например <code>50000</code> (минус, чтобы снять).", "g:"+p.arg)
			return
		}
		if err := s.goals.Deposit(ctx, id, amount); err != nil {
			fail("goal deposit", err)
			return
		}
		s.sendRoute(ctx, b, "g:"+p.arg)
	}
}

// parseGoal reads "name; target; monthly" (monthly optional).
func parseGoal(text string) (name string, target, monthly int64, err error) {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ';' || r == '\n' })
	if len(parts) < 2 {
		return "", 0, 0, fmt.Errorf("Нужен формат: <code>название; цель; в месяц</code>, например <code>Отпуск; 1500000; 150000</code>")
	}
	name = strings.TrimSpace(parts[0])
	if r := []rune(name); len(r) == 0 || len(r) > 40 {
		return "", 0, 0, fmt.Errorf("Название — от 1 до 40 символов.")
	}
	if target, err = ledgersvc.ParseAmount(parts[1]); err != nil || target <= 0 {
		return "", 0, 0, fmt.Errorf("Не понял сумму цели: <code>%s</code>", html.EscapeString(strings.TrimSpace(parts[1])))
	}
	if len(parts) > 2 && strings.TrimSpace(parts[2]) != "" {
		if monthly, err = ledgersvc.ParseAmount(parts[2]); err != nil || monthly < 0 {
			return "", 0, 0, fmt.Errorf("Не понял сумму в месяц: <code>%s</code>", html.EscapeString(strings.TrimSpace(parts[2])))
		}
	}
	return name, target, monthly, nil
}

func (s *Service) handleCommand(ctx context.Context, b *tgbot.Bot, text string) {
	head, args, _ := strings.Cut(text, " ")
	cmd, _, _ := strings.Cut(strings.TrimPrefix(head, "/"), "@")
	args = strings.TrimSpace(args)

	simple := map[string]string{
		"menu": "m", "help": "m", "stats": "ov:m", "time": "tm", "last": "ls", "cards": "cd:m",
		"subs": "sb", "goals": "gl", "charts": "ch", "tags": "tg:m", "shops": "sh:m",
	}
	if route, ok := simple[cmd]; ok {
		s.sendRoute(ctx, b, route)
		return
	}

	switch cmd {
	case "start":
		_, err := b.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID: s.cfg.OwnerUserID,
			Text: "👋 <b>Привет!</b> Я записываю твои траты из Apple Pay и слежу за лимитами.\n\n" +
				"Транзакции прилетают сами. Кнопки внизу всегда под рукой.",
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: bottomKeyboard(),
		})
		if err != nil {
			s.logger.Error("failed to send start", slog.Any("error", err))
		}
		s.sendRoute(ctx, b, "m")
	case "add":
		if args == "" {
			s.sendRoute(ctx, b, "ad")
			return
		}
		s.addManual(ctx, b, args)
	case "find":
		if args == "" {
			s.sendRoute(ctx, b, "fd")
			return
		}
		s.sendFind(ctx, b, args)
	case "export":
		s.sendExport(ctx, b)
	case "budget":
		s.cmdBudget(ctx, b, args)
	case "cat":
		merchant, category, ok := strings.Cut(args, "=")
		merchant, category = strings.TrimSpace(merchant), strings.TrimSpace(category)
		if !ok || merchant == "" || category == "" {
			s.send(ctx, b, "Формат: <code>/cat Magnum = Food &amp; Drinks</code>\n\nИли просто нажми «🏷 Категория» под транзакцией.", menuRow())
			return
		}
		if err := s.rules.Set(ctx, merchant, category); err != nil {
			s.logger.Error("cat failed", slog.Any("error", err))
			s.send(ctx, b, "❌ Ошибка, см. логи.", nil)
			return
		}
		n, err := s.txs.CategorizeMerchant(ctx, merchant, category, uncategorized)
		if err != nil {
			s.logger.Error("cat failed", slog.Any("error", err))
			s.send(ctx, b, "❌ Ошибка, см. логи.", nil)
			return
		}
		s.send(ctx, b, fmt.Sprintf("✅ «%s» → %s. Прошлых трат без категории переразмечено: %d.",
			html.EscapeString(merchant), html.EscapeString(category), n), menuRow())
	default:
		s.sendRoute(ctx, b, "m")
	}
}

// cmdBudget keeps the typed form for power users: /budget <category> <amount|off>.
func (s *Service) cmdBudget(ctx context.Context, b *tgbot.Bot, args string) {
	i := strings.LastIndexByte(args, ' ')
	if args == "" || i < 0 {
		s.sendRoute(ctx, b, "bl")
		return
	}
	category, last := strings.TrimSpace(args[:i]), strings.TrimSpace(args[i+1:])

	if strings.EqualFold(last, "off") {
		if _, err := s.budgets.Delete(ctx, category); err != nil {
			s.logger.Error("budget delete failed", slog.Any("error", err))
			s.send(ctx, b, "❌ Ошибка, см. логи.", nil)
			return
		}
		s.sendRoute(ctx, b, "bl")
		return
	}

	limit, err := ledgersvc.ParseAmount(last)
	if err != nil || limit <= 0 {
		s.send(ctx, b, "Не понял сумму лимита: "+html.EscapeString(last), menuRow())
		return
	}
	if err := s.budgets.Set(ctx, category, limit); err != nil {
		s.logger.Error("budget set failed", slog.Any("error", err))
		s.send(ctx, b, "❌ Ошибка, см. логи.", nil)
		return
	}
	s.sendRoute(ctx, b, "b:"+catKey(category))
}

func (s *Service) sendExport(ctx context.Context, b *tgbot.Bot) {
	txs, err := s.txs.All(ctx)
	if err != nil {
		s.logger.Error("export: load failed", slog.Any("error", err))
		s.send(ctx, b, "❌ Ошибка, см. логи.", nil)
		return
	}
	if len(txs) == 0 {
		s.send(ctx, b, "Пока нечего экспортировать.", menuRow())
		return
	}
	data, err := exportsvc.BuildXLSX(txs, s.cfg.DefaultCurrency, s.cfg.Location)
	if err != nil {
		s.logger.Error("export: build failed", slog.Any("error", err))
		s.send(ctx, b, "❌ Не получилось собрать файл, см. логи.", nil)
		return
	}

	name := fmt.Sprintf("transactions_%s.xlsx", time.Now().In(s.cfg.Location).Format("2006-01-02"))
	_, err = b.SendDocument(ctx, &tgbot.SendDocumentParams{
		ChatID:   s.cfg.OwnerUserID,
		Document: &models.InputFileUpload{Filename: name, Data: bytes.NewReader(data)},
		Caption:  fmt.Sprintf("📤 Экспорт: %d транзакций", len(txs)),
	})
	if err != nil {
		s.logger.Error("export: send failed", slog.Any("error", err))
		s.send(ctx, b, "❌ Не получилось отправить файл, см. логи.", nil)
	}
}
