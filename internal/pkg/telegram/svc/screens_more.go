package svc

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	analyticssvc "pieceomoney/internal/pkg/analytics/svc"
	chartssvc "pieceomoney/internal/pkg/charts/svc"
	ledgersvc "pieceomoney/internal/pkg/ledger/svc"
	"pieceomoney/internal/pkg/storage/repo"
)

// groupScreen renders a ranked breakdown (cards, merchants, tags) of one
// period with the d / w / m / pm switcher.
func (s *Service) groupScreen(ctx context.Context, kind, periodKey, title, icon, empty string, limit int,
	group func([]repo.Transaction, string) []analyticssvc.Group, extra ...[]models.InlineKeyboardButton) (screen, error) {

	p := s.period(periodKey)
	txs, err := s.txs.Between(ctx, p.since, p.until)
	if err != nil {
		return screen{}, err
	}
	groups := group(txs, s.cfg.DefaultCurrency)

	var total int64
	for _, g := range groups {
		total += g.AmountMinor
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s · %s</b>", icon, title, html.EscapeString(p.label))
	if len(groups) == 0 {
		b.WriteString("\n\n" + empty)
	} else {
		fmt.Fprintf(&b, "\n\nВсего: <b>%s</b>\n", s.money(total))
		for _, g := range groups[:min(len(groups), limit)] {
			fmt.Fprintf(&b, "\n%s %3d%%  %s — %s (%d)", bar(g.AmountMinor, total), pct(g.AmountMinor, total),
				html.EscapeString(g.Key), ledgersvc.FormatAmount(g.AmountMinor), g.Count)
		}
	}

	rows := buttons{periodRow(kind, p.key)}
	rows = append(rows, extra...)
	rows = append(rows, []models.InlineKeyboardButton{btn("🏠 Меню", "m")})
	return screen{text: b.String(), rows: rows}, nil
}

func (s *Service) cardsScreen(ctx context.Context, key string) (screen, error) {
	return s.groupScreen(ctx, "cd", key, "Карты", "💳", "В этом периоде трат нет.", 10, analyticssvc.ByCard)
}

func (s *Service) shopsScreen(ctx context.Context, key string) (screen, error) {
	return s.groupScreen(ctx, "sh", key, "Топ магазинов", "🏪", "В этом периоде трат нет.", 12, analyticssvc.ByMerchant,
		[]models.InlineKeyboardButton{btn("🔎 Поиск", "fd")})
}

func (s *Service) tagsScreen(ctx context.Context, key string) (screen, error) {
	return s.groupScreen(ctx, "tg", key, "Теги", "#️⃣", "Тегов в этом периоде нет. Добавь тег кнопкой «#️⃣ Теги» под тратой, например «командировка» или «подарок».", 15, analyticssvc.ByTag,
		[]models.InlineKeyboardButton{btn("🔎 Поиск по тегу", "fd")})
}

// ---- recurring payments ----

var everyLabel = map[string]string{"weekly": "раз в неделю", "monthly": "раз в месяц"}

func (s *Service) subscriptionsScreen(ctx context.Context) (screen, error) {
	now := time.Now()
	txs, err := s.txs.Between(ctx, now.AddDate(0, 0, -400), now.Add(time.Hour))
	if err != nil {
		return screen{}, err
	}
	rec := analyticssvc.DetectRecurring(txs, now)

	var b strings.Builder
	b.WriteString("🔁 <b>Регулярные платежи</b>")
	if len(rec) == 0 {
		b.WriteString("\n\nПока ничего не нашёл. Я ищу платежи в один магазин на похожую сумму, повторяющиеся раз в месяц (от 3 раз) или раз в неделю (от 4 раз).")
		return screen{text: b.String(), rows: menuRow()}, nil
	}

	var monthly int64
	b.WriteString("\n")
	for _, r := range rec {
		days := int(r.Next.Sub(now).Hours() / 24)
		when := fmt.Sprintf("через %d дн.", days)
		switch {
		case days < 0:
			when = fmt.Sprintf("ждали %d дн. назад", -days)
		case days == 0:
			when = "сегодня"
		}
		fmt.Fprintf(&b, "\n📅 %s · <b>%s</b> — %s %s · %s · %s",
			r.Next.In(s.cfg.Location).Format("02.01"), html.EscapeString(r.Merchant),
			ledgersvc.FormatAmount(r.AmountMinor), html.EscapeString(r.Currency), everyLabel[r.Label], when)
		if r.Currency == s.cfg.DefaultCurrency {
			monthly += r.MonthlyCost()
		}
	}
	fmt.Fprintf(&b, "\n\nИтого в месяц: <b>≈ %s</b>\n<i>Определено автоматически по повторяющимся платежам.</i>", s.money(monthly))
	return screen{text: b.String(), rows: buttons{{btn("🏪 Магазины", "sh:m"), btn("🏠 Меню", "m")}}}, nil
}

// ---- search ----

func (s *Service) findPromptScreen() screen {
	s.setPending(pendFind, "")
	return screen{
		text: "🔎 <b>Поиск</b>\n\nПришли слово: магазин, категорию или кусок заметки. Чтобы искать по тегу, начни с #, например <code>#командировка</code>.",
		rows: buttons{{btn("✖️ Отмена", "m")}},
	}
}

// sendFind answers a search with totals, top merchants and the latest hits.
func (s *Service) sendFind(ctx context.Context, b *tgbot.Bot, query string) {
	all, err := s.txs.All(ctx)
	if err != nil {
		s.logger.Error("find failed", "error", err)
		s.send(ctx, b, "❌ Ошибка, см. логи.", menuRow())
		return
	}
	hits := analyticssvc.Search(all, query)
	again := buttons{{btn("🔎 Ещё поиск", "fd"), btn("🏠 Меню", "m")}}
	if len(hits) == 0 {
		s.send(ctx, b, "🔎 По запросу «"+html.EscapeString(query)+"» ничего не нашёл.", again)
		return
	}

	cur := s.cfg.DefaultCurrency
	monthStart := s.monthStart(time.Now())
	var total, month int64
	for _, h := range hits {
		if h.Currency != cur {
			continue
		}
		total += h.AmountMinor
		if !h.TS.Before(monthStart) {
			month += h.AmountMinor
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "🔎 <b>«%s»</b>\n\nНайдено платежей: %d\nВсего: <b>%s</b> · в этом месяце: <b>%s</b>",
		html.EscapeString(query), len(hits), s.money(total), s.money(month))

	if shops := analyticssvc.ByMerchant(hits, cur); len(shops) > 0 {
		out.WriteString("\n\n<b>Где:</b>")
		for _, g := range shops[:min(len(shops), 5)] {
			fmt.Fprintf(&out, "\n• %s — %s (%d)", html.EscapeString(g.Key), ledgersvc.FormatAmount(g.AmountMinor), g.Count)
		}
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].TS.After(hits[j].TS) })
	out.WriteString("\n\n<b>Последние:</b>")
	for _, h := range hits[:min(len(hits), 5)] {
		fmt.Fprintf(&out, "\n%s — %s %s — %s", h.TS.In(s.cfg.Location).Format("02.01.06"),
			ledgersvc.FormatAmount(h.AmountMinor), html.EscapeString(h.Currency), html.EscapeString(txTitle(h)))
	}
	s.send(ctx, b, out.String(), again)
}

// ---- notes and tags ----

func (s *Service) notePromptScreen(ctx context.Context, id int64) (screen, error) {
	t, ok, err := s.txs.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.lastScreen(ctx)
	}
	s.setPending(pendNote, strconv.FormatInt(id, 10))

	text := "📝 <b>Заметка</b>\n\n" + html.EscapeString(txTitle(t)) + " · " + ledgersvc.FormatAmount(t.AmountMinor)
	if t.Note != "" {
		text += "\n\nСейчас: " + html.EscapeString(t.Note)
	}
	text += "\n\nПришли текст заметки. «-» удалит её."
	return screen{text: text, rows: buttons{{btn("✖️ Отмена", fmt.Sprintf("t:%d", id))}}}, nil
}

func (s *Service) tagsPromptScreen(ctx context.Context, id int64) (screen, error) {
	t, ok, err := s.txs.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.lastScreen(ctx)
	}
	s.setPending(pendTags, strconv.FormatInt(id, 10))

	text := "#️⃣ <b>Теги</b>\n\n" + html.EscapeString(txTitle(t)) + " · " + ledgersvc.FormatAmount(t.AmountMinor)
	if t.Tags != "" {
		text += "\n\nСейчас: #" + strings.ReplaceAll(html.EscapeString(t.Tags), ",", " #")
	}
	if all, err := s.txs.All(ctx); err == nil {
		if used := analyticssvc.ByTag(all, s.cfg.DefaultCurrency); len(used) > 0 {
			names := make([]string, 0, 8)
			for _, g := range used[:min(len(used), 8)] {
				names = append(names, "#"+html.EscapeString(g.Key))
			}
			text += "\n\nУже используешь: " + strings.Join(names, " ")
		}
	}
	text += "\n\nПришли теги через пробел или запятую, например <code>командировка подарок</code>. «-» удалит все."
	return screen{text: text, rows: buttons{{btn("✖️ Отмена", fmt.Sprintf("t:%d", id))}}}, nil
}

// ---- charts ----

var monthShort = [...]string{"", "янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

func (s *Service) chartsScreen() screen {
	return screen{
		text: "📈 <b>Графики</b>\n\nВыбери график, пришлю картинкой.",
		rows: buttons{
			{btn("🥧 Категории · месяц", "pic:pie:m"), btn("🥧 Прошлый месяц", "pic:pie:pm")},
			{btn("🥧 Категории · неделя", "pic:pie:w"), btn("📊 По дням месяца", "pic:days")},
			{btn("📈 Тренд за 6 месяцев", "pic:trend")},
			{btn("🏠 Меню", "m")},
		},
	}
}

// sendChart renders and sends one chart; spec is "pie:<period>", "days" or "trend".
func (s *Service) sendChart(ctx context.Context, b *tgbot.Bot, spec string) {
	data, caption, err := s.renderChart(ctx, spec)
	if err != nil {
		s.logger.Error("chart failed", "spec", spec, "error", err)
		s.send(ctx, b, "❌ Не получилось построить график, см. логи.", menuRow())
		return
	}
	if data == nil {
		s.send(ctx, b, "Для этого графика пока нет данных.", buttons{{btn("‹ Графики", "ch"), btn("🏠 Меню", "m")}})
		return
	}
	_, err = b.SendPhoto(ctx, &tgbot.SendPhotoParams{
		ChatID:    s.cfg.OwnerUserID,
		Photo:     &models.InputFileUpload{Filename: "chart.png", Data: bytes.NewReader(data)},
		Caption:   caption,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		s.logger.Error("failed to send chart", "error", err)
	}
}

// renderChart returns nil data when there's nothing to plot.
func (s *Service) renderChart(ctx context.Context, spec string) ([]byte, string, error) {
	cur := s.cfg.DefaultCurrency
	now := time.Now().In(s.cfg.Location)
	kind, arg, _ := strings.Cut(spec, ":")

	switch kind {
	case "pie":
		p := s.period(arg)
		cats, err := s.txs.CategoryTotalsBetween(ctx, p.since, p.until, cur)
		if err != nil {
			return nil, "", err
		}
		var slices []chartssvc.Slice
		var total int64
		for _, c := range cats {
			if c.AmountMinor > 0 {
				slices = append(slices, chartssvc.Slice{Label: c.Category, Amount: c.AmountMinor})
				total += c.AmountMinor
			}
		}
		if len(slices) == 0 {
			return nil, "", nil
		}
		data, err := chartssvc.Pie(slices, func(v int64) string { return ledgersvc.FormatAmount(v) + " " + cur }, "Другое")
		return data, fmt.Sprintf("🥧 <b>Категории · %s</b>\nВсего: %s", html.EscapeString(p.label), s.money(total)), err

	case "days":
		start := s.monthStart(now)
		days := start.AddDate(0, 1, 0).Sub(start).Hours() / 24
		txs, err := s.txs.Between(ctx, start, start.AddDate(0, 1, 0))
		if err != nil {
			return nil, "", err
		}
		totals := analyticssvc.DayTotals(txs, cur, s.cfg.Location, start, int(days+0.5))
		var sum int64
		bars := make([]chartssvc.Bar, len(totals))
		for i, v := range totals {
			bars[i] = chartssvc.Bar{Label: strconv.Itoa(i + 1), Amount: v}
			sum += v
		}
		if sum == 0 {
			return nil, "", nil
		}
		data, err := chartssvc.Bars(bars, 1100, 560)
		return data, fmt.Sprintf("📊 <b>Траты по дням · %s %d</b>\nВсего: %s (%s)", monthNames[now.Month()], now.Year(), s.money(sum), html.EscapeString(cur)), err

	case "trend":
		first := s.monthStart(now).AddDate(0, -5, 0)
		txs, err := s.txs.Between(ctx, first, s.monthStart(now).AddDate(0, 1, 0))
		if err != nil {
			return nil, "", err
		}
		months := analyticssvc.MonthTotals(txs, cur, s.cfg.Location, now, 6)
		var sum int64
		bars := make([]chartssvc.Bar, len(months))
		for i, m := range months {
			bars[i] = chartssvc.Bar{Label: monthShort[m.Start.Month()], Amount: m.AmountMinor}
			sum += m.AmountMinor
		}
		if sum == 0 {
			return nil, "", nil
		}
		data, err := chartssvc.Bars(bars, 1000, 560)
		return data, fmt.Sprintf("📈 <b>Траты по месяцам</b>\nЗа полгода: %s", s.money(sum)), err
	}
	return nil, "", fmt.Errorf("unknown chart %q", spec)
}
