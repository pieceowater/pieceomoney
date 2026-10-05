package svc

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	ledgersvc "pieceomoney/internal/pkg/ledger/svc"
)

func (s *Service) goalsScreen(ctx context.Context) (screen, error) {
	list, err := s.goals.List(ctx)
	if err != nil {
		return screen{}, err
	}

	rows := buttons{}
	for _, g := range list {
		saved, err := s.goals.SavedSince(ctx, g.ID, time.Time{})
		if err != nil {
			return screen{}, err
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(
			fmt.Sprintf("🐷 %s · %d%%", g.Name, pct(saved, g.TargetMinor)), fmt.Sprintf("g:%d", g.ID))})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("➕ Новая цель", "gn")}, []models.InlineKeyboardButton{btn("🏠 Меню", "m")})

	text := "🐷 <b>Цели накоплений</b>\n\nОткладывай на цель и смотри, сколько осталось и когда дойдёшь до неё."
	if len(list) == 0 {
		text = "🐷 <b>Цели накоплений</b>\n\nЦелей пока нет. Добавь первую: название, сколько нужно и сколько откладывать в месяц."
	}
	return screen{text: text, rows: rows}, nil
}

func (s *Service) goalScreen(ctx context.Context, id int64, toast string) (screen, error) {
	g, ok, err := s.goals.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.goalsScreen(ctx)
	}
	saved, err := s.goals.SavedSince(ctx, id, time.Time{})
	if err != nil {
		return screen{}, err
	}
	thisMonth, err := s.goals.SavedSince(ctx, id, s.monthStart(time.Now()))
	if err != nil {
		return screen{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🐷 <b>%s</b>\n\n%s %d%%\nНакоплено: <b>%s</b> из %s",
		html.EscapeString(g.Name), bar(saved, g.TargetMinor), pct(saved, g.TargetMinor), s.money(saved), s.money(g.TargetMinor))

	remaining := g.TargetMinor - saved
	switch {
	case remaining <= 0:
		b.WriteString("\n\n🎉 <b>Цель достигнута!</b>")
	default:
		fmt.Fprintf(&b, "\nОсталось: <b>%s</b>", s.money(remaining))
	}
	if g.MonthlyMinor > 0 {
		fmt.Fprintf(&b, "\n\nВ этом месяце: %s / %s (%d%%)", ledgersvc.FormatAmount(thisMonth), s.money(g.MonthlyMinor), pct(thisMonth, g.MonthlyMinor))
		if remaining > 0 {
			months := int((remaining + g.MonthlyMinor - 1) / g.MonthlyMinor)
			eta := time.Now().In(s.cfg.Location).AddDate(0, months, 0)
			fmt.Fprintf(&b, "\nПри таком темпе: ≈ %d мес. (%s %d)", months, monthNames[eta.Month()], eta.Year())
		}
	}

	key := strconv.FormatInt(id, 10)
	first := []models.InlineKeyboardButton{btn("➕ Отложить", "gp:"+key)}
	if g.MonthlyMinor > 0 && remaining > 0 {
		first = append(first, btn("+ "+ledgersvc.FormatAmount(g.MonthlyMinor), "gq:"+key+":m"))
	}
	return screen{toast: toast, text: b.String(), rows: buttons{
		first,
		{btn("✏️ Изменить", "ge:"+key), btn("🗑 Удалить", "gd:"+key)},
		{btn("‹ Цели", "gl"), btn("🏠 Меню", "m")},
	}}, nil
}

func (s *Service) goalNewPromptScreen() screen {
	s.setPending(pendGoalNew, "")
	return screen{
		text: fmt.Sprintf("➕ <b>Новая цель</b>\n\nПришли одной строкой: <code>название; цель; в месяц</code>\nНапример: <code>Отпуск; 1500000; 150000</code>\n\nСуммы в %s. «В месяц» можно не указывать.",
			html.EscapeString(s.cfg.DefaultCurrency)),
		rows: buttons{{btn("✖️ Отмена", "gl")}},
	}
}

func (s *Service) goalEditPromptScreen(ctx context.Context, id int64) (screen, error) {
	g, ok, err := s.goals.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.goalsScreen(ctx)
	}
	s.setPending(pendGoalEdit, strconv.FormatInt(id, 10))
	return screen{
		text: fmt.Sprintf("✏️ <b>Изменить цель</b>\n\nСейчас: <code>%s; %d; %d</code>\n\nПришли новую строку в том же формате: <code>название; цель; в месяц</code>. Накопленное сохранится.",
			html.EscapeString(g.Name), g.TargetMinor/100, g.MonthlyMinor/100),
		rows: buttons{{btn("✖️ Отмена", fmt.Sprintf("g:%d", id))}},
	}, nil
}

func (s *Service) goalDepositPromptScreen(ctx context.Context, id int64) (screen, error) {
	g, ok, err := s.goals.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.goalsScreen(ctx)
	}
	s.setPending(pendGoalDep, strconv.FormatInt(id, 10))

	key := strconv.FormatInt(id, 10)
	var presets []models.InlineKeyboardButton
	if g.MonthlyMinor > 0 {
		presets = append(presets, btn(ledgersvc.FormatAmount(g.MonthlyMinor), "gq:"+key+":m"))
	}
	for _, k := range []int{10, 50, 100} {
		presets = append(presets, btn(fmt.Sprintf("%dk", k), fmt.Sprintf("gq:%s:%d", key, k)))
	}
	return screen{
		text: fmt.Sprintf("➕ <b>Отложить на «%s»</b>\n\nВыбери сумму или пришли свою. Минус, например <code>-20000</code>, снимет деньги с цели.", html.EscapeString(g.Name)),
		rows: buttons{presets, {btn("✖️ Отмена", "g:"+key)}},
	}, nil
}

func (s *Service) goalQuickDeposit(ctx context.Context, id int64, amount string) (screen, error) {
	g, ok, err := s.goals.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.goalsScreen(ctx)
	}
	var minor int64
	if amount == "m" {
		minor = g.MonthlyMinor
	} else if k, err := strconv.ParseInt(amount, 10, 64); err == nil {
		minor = k * 1000 * 100
	}
	if minor <= 0 {
		return s.goalScreen(ctx, id, "")
	}
	if err := s.goals.Deposit(ctx, id, minor); err != nil {
		return screen{}, err
	}
	return s.goalScreen(ctx, id, "Отложено "+ledgersvc.FormatAmount(minor))
}

func (s *Service) goalDeleteScreen(ctx context.Context, id int64, confirmed bool) (screen, error) {
	g, ok, err := s.goals.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.goalsScreen(ctx)
	}
	if !confirmed {
		return screen{
			text: fmt.Sprintf("🗑 Удалить цель «%s»? Вместе с историей накоплений.", html.EscapeString(g.Name)),
			rows: buttons{{btn("Да, удалить", fmt.Sprintf("gdy:%d", id)), btn("Отмена", fmt.Sprintf("g:%d", id))}},
		}, nil
	}
	if err := s.goals.Delete(ctx, id); err != nil {
		return screen{}, err
	}
	sc, err := s.goalsScreen(ctx)
	sc.toast = "Цель удалена"
	return sc, err
}
