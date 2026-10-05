package svc

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

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

	// A pending "send me the limit amount" prompt swallows the next plain message.
	if pending := s.takePending(); pending != "" && !strings.HasPrefix(text, "/") {
		limit, err := ledgersvc.ParseAmount(text)
		if err != nil || limit <= 0 {
			s.setPending(pending)
			s.send(ctx, b, "Не понял сумму. Пришли число, например <code>150000</code>.", [][]models.InlineKeyboardButton{
				{btn("✖️ Отмена", "b:"+catKey(pending))},
			})
			return true
		}
		if err := s.budgets.Set(ctx, pending, limit); err != nil {
			s.logger.Error("set budget failed", slog.Any("error", err))
			s.send(ctx, b, "❌ Ошибка, см. логи.", nil)
			return true
		}
		s.sendRoute(ctx, b, "b:"+catKey(pending))
		return true
	}

	switch text {
	case kbOverview:
		s.sendRoute(ctx, b, "ov:m")
		return true
	case kbBudgets:
		s.sendRoute(ctx, b, "bl")
		return true
	case kbLast:
		s.sendRoute(ctx, b, "ls")
		return true
	case kbMenu:
		s.sendRoute(ctx, b, "m")
		return true
	}

	if !strings.HasPrefix(text, "/") {
		return false
	}
	s.handleCommand(ctx, b, text)
	return true
}

func (s *Service) handleCommand(ctx context.Context, b *tgbot.Bot, text string) {
	head, args, _ := strings.Cut(text, " ")
	cmd, _, _ := strings.Cut(strings.TrimPrefix(head, "/"), "@")
	args = strings.TrimSpace(args)

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
	case "menu", "help":
		s.sendRoute(ctx, b, "m")
	case "stats":
		s.sendRoute(ctx, b, "ov:m")
	case "time":
		s.sendRoute(ctx, b, "tm")
	case "export":
		s.sendExport(ctx, b)
	case "last":
		s.sendRoute(ctx, b, "ls")
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
