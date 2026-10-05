// Package internal wires everything together -- one place that knows how
// every service connects, Start()/Stop() as the only public surface main.go
// touches.
package internal

import (
	"context"
	"log/slog"
	"os"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"pieceomoney/internal/core/cfg"
	"pieceomoney/internal/pkg/storage/repo"
	telegramsvc "pieceomoney/internal/pkg/telegram/svc"
)

var allowedUpdates = tgbot.AllowedUpdates{"message", "callback_query", "poll"}

var ownerCommands = []models.BotCommand{
	{Command: "menu", Description: "Главное меню"},
	{Command: "stats", Description: "Обзор трат"},
	{Command: "budget", Description: "Лимиты по категориям"},
	{Command: "last", Description: "Последние траты"},
	{Command: "time", Description: "Траты по времени"},
	{Command: "find", Description: "Поиск по тратам"},
	{Command: "cards", Description: "Траты по картам"},
	{Command: "subs", Description: "Регулярные платежи"},
	{Command: "goals", Description: "Цели накоплений"},
	{Command: "charts", Description: "Графики"},
	{Command: "export", Description: "Экспорт всех трат в Excel"},
}

type App struct {
	logger *slog.Logger
	cfg    *cfg.Config
	bot    *tgbot.Bot
	ctx    context.Context
	cancel context.CancelFunc
}

func NewApp() *App {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	c := cfg.Inst()

	sqlDB, err := repo.Connect(c.DBPath)
	if err != nil {
		logger.Error("failed to open database", slog.Any("error", err))
		os.Exit(1)
	}

	telegram := telegramsvc.New(c, logger, repo.NewTransactionsRepo(sqlDB), repo.NewBudgetsRepo(sqlDB), repo.NewRulesRepo(sqlDB), repo.NewGoalsRepo(sqlDB))

	b, err := tgbot.New(c.BotToken,
		tgbot.WithDefaultHandler(telegram.HandleUpdate),
		tgbot.WithAllowedUpdates(allowedUpdates),
		tgbot.WithErrorsHandler(func(err error) {
			logger.Error("bot error", slog.Any("error", err))
		}),
	)
	if err != nil {
		logger.Error("failed to create bot", slog.Any("error", err))
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &App{logger: logger, cfg: c, bot: b, ctx: ctx, cancel: cancel}
}

// Start blocks until Stop cancels it. Long polling, so no public address is
// needed.
func (a *App) Start() {
	a.registerOwnerCommands()
	a.logger.Info("pieceomoney started", slog.Int64("owner_user_id", a.cfg.OwnerUserID))
	a.bot.Start(a.ctx)
	a.logger.Info("pieceomoney stopped")
}

func (a *App) registerOwnerCommands() {
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	_, err := a.bot.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{
		Commands: ownerCommands,
		Scope:    &models.BotCommandScopeChat{ChatID: a.cfg.OwnerUserID},
	})
	if err != nil {
		a.logger.Warn("failed to register owner command menu", slog.Any("error", err))
	}
}

func (a *App) Stop() {
	a.cancel()
}
