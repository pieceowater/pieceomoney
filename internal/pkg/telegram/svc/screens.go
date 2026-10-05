package svc

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	ledgersvc "pieceomoney/internal/pkg/ledger/svc"
	"pieceomoney/internal/pkg/storage/repo"
)

type buttons = [][]models.InlineKeyboardButton

type screen struct {
	text  string
	rows  buttons
	toast string
}

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func menuRow() buttons { return buttons{{btn("🏠 Меню", "m")}} }

// defaultCategories are Apple Wallet's category names, offered even before
// the first payment in them.
var defaultCategories = []string{"Food & Drinks", "Shopping", "Travel", "Services", "Entertainment", "Health"}

// catKey is a short stable id for a category, because callback data is
// limited to 64 bytes and category names can be long or non-ASCII.
func catKey(name string) string {
	h := sha1.Sum([]byte(strings.ToLower(name)))
	return hex.EncodeToString(h[:4])
}

func catEmoji(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "food"), strings.Contains(n, "drink"), strings.Contains(n, "еда"), strings.Contains(n, "продукт"):
		return "🍽"
	case strings.Contains(n, "shop"), strings.Contains(n, "покуп"):
		return "🛍"
	case strings.Contains(n, "travel"), strings.Contains(n, "путеш"), strings.Contains(n, "транспорт"):
		return "✈️"
	case strings.Contains(n, "service"), strings.Contains(n, "услуг"):
		return "🧰"
	case strings.Contains(n, "entertain"), strings.Contains(n, "развлеч"):
		return "🎬"
	case strings.Contains(n, "health"), strings.Contains(n, "здоров"):
		return "💊"
	}
	return "🏷"
}

func statusDot(spent, limit int64) string {
	switch p := pct(spent, limit); {
	case p >= 90:
		return "🔴"
	case p >= 70:
		return "🟡"
	}
	return "🟢"
}

// knownCategories returns categories offered in pickers, defaults first.
func (s *Service) knownCategories(ctx context.Context) ([]string, error) {
	seen := map[string]bool{strings.ToLower(uncategorized): true}
	var out []string
	add := func(c string) {
		if k := strings.ToLower(c); !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	for _, c := range defaultCategories {
		add(c)
	}
	rest, err := s.txs.KnownCategories(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(rest)
	for _, c := range rest {
		add(c)
	}
	return out, nil
}

// resolveCategory maps a catKey back to the category name, including the
// uncategorized placeholder.
func (s *Service) resolveCategory(ctx context.Context, key string) (string, bool, error) {
	if key == catKey(uncategorized) {
		return uncategorized, true, nil
	}
	all, err := s.knownCategories(ctx)
	if err != nil {
		return "", false, err
	}
	for _, c := range all {
		if catKey(c) == key {
			return c, true, nil
		}
	}
	return "", false, nil
}

func (s *Service) sendRoute(ctx context.Context, b *tgbot.Bot, data string) {
	sc, err := s.route(ctx, data)
	if err != nil {
		s.logger.Error("route failed", "route", data, "error", err)
		s.send(ctx, b, "❌ Ошибка, см. логи.", menuRow())
		return
	}
	s.send(ctx, b, sc.text, sc.rows)
}

func (s *Service) handleCallback(ctx context.Context, b *tgbot.Bot, cq *models.CallbackQuery) {
	if cq.From.ID != s.cfg.OwnerUserID || cq.Message.Message == nil {
		return
	}
	msg := cq.Message.Message
	s.setPending("", "")

	if cq.Data == "ex" {
		_, _ = b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: "Готовлю файл…"})
		s.sendExport(ctx, b)
		return
	}
	if spec, ok := strings.CutPrefix(cq.Data, "pic:"); ok {
		_, _ = b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: "Рисую…"})
		s.sendChart(ctx, b, spec)
		return
	}

	sc, err := s.route(ctx, cq.Data)
	if err != nil {
		s.logger.Error("route failed", "route", cq.Data, "error", err)
		_, _ = b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: "Ошибка, см. логи"})
		return
	}
	_, _ = b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: sc.toast})

	_, err = b.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:      msg.Chat.ID,
		MessageID:   msg.ID,
		Text:        sc.text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: sc.rows},
	})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		s.logger.Error("failed to edit message", "error", err)
	}
}

// route renders the screen for a callback data string, applying any
// mutation the button stands for first.
//
//	m                     main menu
//	ov:<p>  cs:<p>        overview / category list for period p (d, w, m, pm)
//	c:<key>:<p>           one category
//	tm                    time analytics
//	bl  b:<key>           limits list / one limit
//	ba                    pick a category for a new limit
//	bs:<key>              prompt for a limit amount
//	bp:<key>:<k>          set limit to k thousand
//	bd:<key>  bdy:<key>   delete limit (confirm / do)
//	ls  t:<id>            recent payments / one payment
//	tc:<id>  tk:<id>:<key>  choose / apply a payment's category
//	td:<id>  tdy:<id>     delete payment (confirm / do)
//	tn:<id>  tt:<id>      prompt for a payment's note / tags
//	cd:<p> sh:<p> tg:<p>  cards / top merchants / tags for period p
//	sb  fd  ch            recurring payments / search prompt / charts menu
//	gl g:<id> gn          goals list / one goal / new goal prompt
//	ge:<id> gp:<id>       goal edit / deposit prompt
//	gq:<id>:<k|m>         quick deposit (k thousand, or the monthly amount)
//	gd:<id> gdy:<id>      delete goal (confirm / do)
//	pic:...               chart image, handled in handleCallback
func (s *Service) route(ctx context.Context, data string) (screen, error) {
	parts := strings.Split(data, ":")
	arg := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	id := func() int64 { v, _ := strconv.ParseInt(arg(1), 10, 64); return v }

	switch parts[0] {
	case "ov":
		return s.overviewScreen(ctx, arg(1))
	case "cs":
		return s.categoriesScreen(ctx, arg(1))
	case "c":
		return s.categoryScreen(ctx, arg(1), arg(2))
	case "tm":
		return s.timeScreen(ctx)
	case "bl":
		return s.budgetsScreen(ctx)
	case "b":
		return s.budgetScreen(ctx, arg(1), "")
	case "ba":
		return s.budgetPickScreen(ctx)
	case "bs":
		return s.budgetPromptScreen(ctx, arg(1))
	case "bp":
		return s.budgetPreset(ctx, arg(1), arg(2))
	case "bd":
		return s.budgetDeleteScreen(ctx, arg(1), false)
	case "bdy":
		return s.budgetDeleteScreen(ctx, arg(1), true)
	case "ls":
		return s.lastScreen(ctx)
	case "cd":
		return s.cardsScreen(ctx, arg(1))
	case "sh":
		return s.shopsScreen(ctx, arg(1))
	case "tg":
		return s.tagsScreen(ctx, arg(1))
	case "sb":
		return s.subscriptionsScreen(ctx)
	case "fd":
		return s.findPromptScreen(), nil
	case "ch":
		return s.chartsScreen(), nil
	case "tn":
		return s.notePromptScreen(ctx, id())
	case "tt":
		return s.tagsPromptScreen(ctx, id())
	case "gl":
		return s.goalsScreen(ctx)
	case "g":
		return s.goalScreen(ctx, id(), "")
	case "gn":
		return s.goalNewPromptScreen(), nil
	case "ge":
		return s.goalEditPromptScreen(ctx, id())
	case "gp":
		return s.goalDepositPromptScreen(ctx, id())
	case "gq":
		return s.goalQuickDeposit(ctx, id(), arg(2))
	case "gd":
		return s.goalDeleteScreen(ctx, id(), false)
	case "gdy":
		return s.goalDeleteScreen(ctx, id(), true)
	case "t":
		return s.txScreen(ctx, id(), "")
	case "tc":
		return s.txCategoryScreen(ctx, id())
	case "tk":
		return s.txSetCategory(ctx, id(), arg(2))
	case "td":
		return s.txDeleteScreen(ctx, id(), false)
	case "tdy":
		return s.txDeleteScreen(ctx, id(), true)
	}
	return s.mainScreen(ctx)
}

// ---- main ----

func (s *Service) mainScreen(ctx context.Context) (screen, error) {
	now := time.Now().In(s.cfg.Location)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.cfg.Location)
	monthStart := s.monthStart(now)
	far := now.AddDate(1, 0, 0)

	month, err := s.defaultTotal(ctx, monthStart, far)
	if err != nil {
		return screen{}, err
	}
	today, err := s.defaultTotal(ctx, dayStart, far)
	if err != nil {
		return screen{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "💰 <b>Мои траты</b>\n\n📆 Месяц: <b>%s</b>\n☀️ Сегодня: <b>%s</b>", s.money(month), s.money(today))

	budgets, err := s.budgets.List(ctx)
	if err != nil {
		return screen{}, err
	}
	var hot []string
	for _, bud := range budgets {
		spent, err := s.txs.CategorySpentSince(ctx, bud.Category, monthStart, s.cfg.DefaultCurrency)
		if err != nil {
			return screen{}, err
		}
		if p := pct(spent, bud.LimitMinor); p >= 90 {
			hot = append(hot, fmt.Sprintf("%s %s — %d%%", statusDot(spent, bud.LimitMinor), html.EscapeString(bud.Category), p))
		}
	}
	if len(hot) > 0 {
		b.WriteString("\n\n⚠️ <b>Лимиты на исходе:</b>\n" + strings.Join(hot, "\n"))
	}

	return screen{text: b.String(), rows: buttons{
		{btn("📊 Обзор", "ov:m"), btn("🏷 Категории", "cs:m")},
		{btn("🎯 Лимиты", "bl"), btn("🐷 Цели", "gl")},
		{btn("💳 Карты", "cd:m"), btn("🔁 Подписки", "sb")},
		{btn("📈 Графики", "ch"), btn("🕐 Время", "tm")},
		{btn("🏪 Магазины", "sh:m"), btn("#️⃣ Теги", "tg:m")},
		{btn("🔎 Поиск", "fd"), btn("🧾 Последние", "ls")},
		{btn("📤 Excel", "ex")},
	}}, nil
}

func (s *Service) defaultTotal(ctx context.Context, since, until time.Time) (int64, error) {
	totals, err := s.txs.TotalsBetween(ctx, since, until)
	if err != nil {
		return 0, err
	}
	for _, t := range totals {
		if t.Currency == s.cfg.DefaultCurrency {
			return t.AmountMinor, nil
		}
	}
	return 0, nil
}

// ---- periods ----

var monthNames = [...]string{"", "январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"}

type period struct {
	key, label string
	since      time.Time
	until      time.Time
}

func (s *Service) period(key string) period {
	now := time.Now().In(s.cfg.Location)
	loc := s.cfg.Location
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch key {
	case "d":
		return period{"d", "Сегодня", day, day.AddDate(0, 0, 1)}
	case "w":
		start := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
		return period{"w", "Неделя", start, start.AddDate(0, 0, 7)}
	case "pm":
		cur := s.monthStart(now)
		prev := cur.AddDate(0, -1, 0)
		return period{"pm", fmt.Sprintf("%s %d", monthNames[prev.Month()], prev.Year()), prev, cur}
	}
	cur := s.monthStart(now)
	return period{"m", fmt.Sprintf("%s %d", monthNames[cur.Month()], cur.Year()), cur, cur.AddDate(0, 1, 0)}
}

// periodRow is the d / w / m / pm switcher; kind is the route prefix it targets.
func periodRow(kind, active string) []models.InlineKeyboardButton {
	items := [...][2]string{{"d", "Сегодня"}, {"w", "Неделя"}, {"m", "Месяц"}, {"pm", "Прошлый"}}
	row := make([]models.InlineKeyboardButton, 0, len(items))
	for _, it := range items {
		label := it[1]
		if it[0] == active {
			label = "• " + label
		}
		row = append(row, btn(label, kind+":"+it[0]))
	}
	return row
}

// ---- overview & categories ----

func (s *Service) overviewScreen(ctx context.Context, key string) (screen, error) {
	p := s.period(key)
	cur := s.cfg.DefaultCurrency

	totals, err := s.txs.TotalsBetween(ctx, p.since, p.until)
	if err != nil {
		return screen{}, err
	}
	cats, err := s.txs.CategoryTotalsBetween(ctx, p.since, p.until, cur)
	if err != nil {
		return screen{}, err
	}

	var total, count int64
	var others []string
	for _, t := range totals {
		if t.Currency == cur {
			total, count = t.AmountMinor, t.Count
		} else {
			others = append(others, fmt.Sprintf("%s %s", ledgersvc.FormatAmount(t.AmountMinor), html.EscapeString(t.Currency)))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>Обзор · %s</b>\n\nПотрачено: <b>%s</b>", html.EscapeString(p.label), s.money(total))
	if count > 0 {
		end := time.Now()
		if p.until.Before(end) {
			end = p.until
		}
		days := int(end.Sub(p.since).Hours()/24) + 1
		fmt.Fprintf(&b, "\nОпераций: %d · в среднем %s/день", count, s.money(total/int64(days)))
	}
	if p.key == "m" {
		prev := s.period("pm")
		if before, err := s.defaultTotal(ctx, prev.since, prev.until); err == nil && before > 0 {
			fmt.Fprintf(&b, "\nПрошлый месяц: %s", s.money(before))
		}
	}
	if len(others) > 0 {
		b.WriteString("\nДругие валюты: " + strings.Join(others, ", "))
	}

	if len(cats) > 0 {
		b.WriteString("\n")
		for _, c := range cats[:min(len(cats), 7)] {
			fmt.Fprintf(&b, "\n%s %s %3d%%  %s", catEmoji(c.Category), bar(c.AmountMinor, total), pct(c.AmountMinor, total),
				html.EscapeString(c.Category))
		}
	} else {
		b.WriteString("\n\nВ этом периоде трат нет.")
	}

	return screen{text: b.String(), rows: buttons{
		periodRow("ov", p.key),
		{btn("🏷 Категории", "cs:"+p.key), btn("🕐 Время", "tm")},
		{btn("🏠 Меню", "m")},
	}}, nil
}

func (s *Service) categoriesScreen(ctx context.Context, key string) (screen, error) {
	p := s.period(key)
	cats, err := s.txs.CategoryTotalsBetween(ctx, p.since, p.until, s.cfg.DefaultCurrency)
	if err != nil {
		return screen{}, err
	}
	var total int64
	for _, c := range cats {
		total += c.AmountMinor
	}

	rows := buttons{periodRow("cs", p.key)}
	for _, c := range cats {
		rows = append(rows, []models.InlineKeyboardButton{btn(
			fmt.Sprintf("%s %s · %d%% · %s", catEmoji(c.Category), c.Category, pct(c.AmountMinor, total), ledgersvc.FormatAmount(c.AmountMinor)),
			fmt.Sprintf("c:%s:%s", catKey(c.Category), p.key))})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("📊 Обзор", "ov:"+p.key), btn("🏠 Меню", "m")})

	text := fmt.Sprintf("🏷 <b>Категории · %s</b>\n\nВсего: <b>%s</b>\nВыбери категорию, чтобы посмотреть детали.", html.EscapeString(p.label), s.money(total))
	if len(cats) == 0 {
		text = fmt.Sprintf("🏷 <b>Категории · %s</b>\n\nВ этом периоде трат нет.", html.EscapeString(p.label))
	}
	return screen{text: text, rows: rows}, nil
}

func (s *Service) categoryScreen(ctx context.Context, catID, periodKey string) (screen, error) {
	category, ok, err := s.resolveCategory(ctx, catID)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.categoriesScreen(ctx, periodKey)
	}
	p := s.period(periodKey)
	cur := s.cfg.DefaultCurrency

	cats, err := s.txs.CategoryTotalsBetween(ctx, p.since, p.until, cur)
	if err != nil {
		return screen{}, err
	}
	var total, spent, count int64
	for _, c := range cats {
		total += c.AmountMinor
		if strings.EqualFold(c.Category, category) {
			spent, count = c.AmountMinor, c.Count
		}
	}
	merchants, err := s.txs.TopMerchantsBetween(ctx, category, p.since, p.until, cur, 5)
	if err != nil {
		return screen{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s · %s</b>\n\n%s %d%% от всех трат\nПотрачено: <b>%s</b> · операций: %d",
		catEmoji(category), html.EscapeString(category), html.EscapeString(p.label),
		bar(spent, total), pct(spent, total), s.money(spent), count)

	if len(merchants) > 0 {
		b.WriteString("\n\n<b>Топ магазинов:</b>")
		for _, m := range merchants {
			fmt.Fprintf(&b, "\n• %s — %s", html.EscapeString(m.Merchant), ledgersvc.FormatAmount(m.AmountMinor))
		}
	}
	if bud, has, err := s.budgets.Get(ctx, category); err == nil && has {
		monthSpent, err := s.txs.CategorySpentSince(ctx, category, s.monthStart(time.Now()), cur)
		if err != nil {
			return screen{}, err
		}
		fmt.Fprintf(&b, "\n\n🎯 Лимит на месяц: %s %d%% (%s / %s)", statusDot(monthSpent, bud.LimitMinor),
			pct(monthSpent, bud.LimitMinor), ledgersvc.FormatAmount(monthSpent), ledgersvc.FormatAmount(bud.LimitMinor))
	}

	limitLabel := "🎯 Поставить лимит"
	limitRoute := "bs:" + catID
	if _, has, _ := s.budgets.Get(ctx, category); has {
		limitLabel, limitRoute = "🎯 Лимит", "b:"+catID
	}
	return screen{text: b.String(), rows: buttons{
		periodRow("c:"+catID, p.key),
		{btn(limitLabel, limitRoute)},
		{btn("‹ Категории", "cs:"+p.key), btn("🏠 Меню", "m")},
	}}, nil
}

// ---- time ----

var weekdayNames = [7]string{"Вс", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}

func (s *Service) timeScreen(ctx context.Context) (screen, error) {
	spends, err := s.txs.SpendsSince(ctx, time.Now().AddDate(0, 0, -90), s.cfg.DefaultCurrency)
	if err != nil {
		return screen{}, err
	}
	back := buttons{{btn("📊 Обзор", "ov:m"), btn("🏠 Меню", "m")}}
	if len(spends) == 0 {
		return screen{text: "🕐 <b>Время трат</b>\n\nПока нет данных.", rows: back}, nil
	}

	var byDay [7]int64
	var byPart [4]int64
	var total int64
	for _, sp := range spends {
		t := sp.TS.In(s.cfg.Location)
		byDay[t.Weekday()] += sp.AmountMinor
		byPart[t.Hour()/6] += sp.AmountMinor
		total += sp.AmountMinor
	}

	parts := [4]string{"🌙 Ночь 00–06", "🌅 Утро 06–12", "☀️ День 12–18", "🌆 Вечер 18–24"}
	var b strings.Builder
	b.WriteString("🕐 <b>Время трат · 90 дней</b>\n\n<b>Время суток</b>")
	for i, v := range byPart {
		fmt.Fprintf(&b, "\n%s %3d%%  %s", bar(v, total), pct(v, total), parts[i])
	}
	b.WriteString("\n\n<b>Дни недели</b>")
	for _, d := range [...]int{1, 2, 3, 4, 5, 6, 0} {
		fmt.Fprintf(&b, "\n%s %3d%%  %s", bar(byDay[d], total), pct(byDay[d], total), weekdayNames[d])
	}
	return screen{text: b.String(), rows: back}, nil
}

// ---- limits ----

func (s *Service) budgetsScreen(ctx context.Context) (screen, error) {
	list, err := s.budgets.List(ctx)
	if err != nil {
		return screen{}, err
	}
	monthStart := s.monthStart(time.Now())

	rows := buttons{}
	for _, bud := range list {
		spent, err := s.txs.CategorySpentSince(ctx, bud.Category, monthStart, s.cfg.DefaultCurrency)
		if err != nil {
			return screen{}, err
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(
			fmt.Sprintf("%s %s %s · %d%%", statusDot(spent, bud.LimitMinor), catEmoji(bud.Category), bud.Category, pct(spent, bud.LimitMinor)),
			"b:"+catKey(bud.Category))})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("➕ Добавить лимит", "ba")}, []models.InlineKeyboardButton{btn("🏠 Меню", "m")})

	text := "🎯 <b>Лимиты на месяц</b>\n\nПредупрежу, когда потрачено 90% лимита, и ещё раз на 100%."
	if len(list) == 0 {
		text = "🎯 <b>Лимиты на месяц</b>\n\nЛимитов пока нет. Добавь первый, и я предупрежу, когда потрачено 90%."
	}
	return screen{text: text, rows: rows}, nil
}

func (s *Service) budgetScreen(ctx context.Context, key, toast string) (screen, error) {
	category, ok, err := s.resolveCategory(ctx, key)
	if err != nil {
		return screen{}, err
	}
	bud, has, err := s.budgets.Get(ctx, category)
	if err != nil {
		return screen{}, err
	}
	if !ok || !has {
		return s.budgetsScreen(ctx)
	}

	now := time.Now().In(s.cfg.Location)
	spent, err := s.txs.CategorySpentSince(ctx, bud.Category, s.monthStart(now), s.cfg.DefaultCurrency)
	if err != nil {
		return screen{}, err
	}
	daysInMonth := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, s.cfg.Location).Day()
	daysLeft := daysInMonth - now.Day() + 1

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b>\n\n%s %s %d%%\n%s из %s",
		catEmoji(bud.Category), html.EscapeString(bud.Category), statusDot(spent, bud.LimitMinor),
		bar(spent, bud.LimitMinor), pct(spent, bud.LimitMinor), s.money(spent), s.money(bud.LimitMinor))

	if left := bud.LimitMinor - spent; left > 0 {
		fmt.Fprintf(&b, "\n\nОсталось: <b>%s</b> · ≈ %s в день до конца месяца", s.money(left), s.money(left/int64(daysLeft)))
	} else {
		fmt.Fprintf(&b, "\n\n🚫 Лимит превышен на <b>%s</b>", s.money(-left))
	}
	if projected := spent * int64(daysInMonth) / int64(now.Day()); spent > 0 {
		fmt.Fprintf(&b, "\n📈 В таком темпе к концу месяца: %s", s.money(projected))
		if projected > bud.LimitMinor {
			b.WriteString(" (выше лимита)")
		}
	}

	key = catKey(bud.Category)
	return screen{toast: toast, text: b.String(), rows: buttons{
		{btn("✏️ Изменить", "bs:"+key), btn("🗑 Удалить", "bd:"+key)},
		{btn("🏷 Детали", "c:"+key+":m")},
		{btn("‹ Лимиты", "bl"), btn("🏠 Меню", "m")},
	}}, nil
}

func (s *Service) budgetPickScreen(ctx context.Context) (screen, error) {
	cats, err := s.knownCategories(ctx)
	if err != nil {
		return screen{}, err
	}
	rows := categoryButtons(cats, func(c string) string { return "bs:" + catKey(c) })
	rows = append(rows, []models.InlineKeyboardButton{btn("‹ Лимиты", "bl")})
	return screen{text: "➕ <b>Новый лимит</b>\n\nДля какой категории?", rows: rows}, nil
}

func (s *Service) budgetPromptScreen(ctx context.Context, key string) (screen, error) {
	category, ok, err := s.resolveCategory(ctx, key)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.budgetsScreen(ctx)
	}
	s.setPending(pendBudget, category)

	presets := []int{50, 100, 150, 200, 300}
	row := make([]models.InlineKeyboardButton, 0, len(presets))
	for _, k := range presets {
		row = append(row, btn(fmt.Sprintf("%dk", k), fmt.Sprintf("bp:%s:%d", key, k)))
	}
	back := "bl"
	if _, has, _ := s.budgets.Get(ctx, category); has {
		back = "b:" + key
	}
	return screen{
		text: fmt.Sprintf("🎯 <b>Лимит · %s %s</b>\n\nВыбери сумму на месяц в %s или пришли своё число сообщением.",
			catEmoji(category), html.EscapeString(category), html.EscapeString(s.cfg.DefaultCurrency)),
		rows: buttons{row, {btn("✖️ Отмена", back)}},
	}, nil
}

func (s *Service) budgetPreset(ctx context.Context, key, thousands string) (screen, error) {
	category, ok, err := s.resolveCategory(ctx, key)
	if err != nil {
		return screen{}, err
	}
	k, convErr := strconv.ParseInt(thousands, 10, 64)
	if !ok || convErr != nil || k <= 0 {
		return s.budgetsScreen(ctx)
	}
	if err := s.budgets.Set(ctx, category, k*1000*100); err != nil {
		return screen{}, err
	}
	return s.budgetScreen(ctx, key, "Лимит сохранён")
}

func (s *Service) budgetDeleteScreen(ctx context.Context, key string, confirmed bool) (screen, error) {
	category, ok, err := s.resolveCategory(ctx, key)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.budgetsScreen(ctx)
	}
	if !confirmed {
		return screen{
			text: fmt.Sprintf("🗑 Убрать лимит «%s»?", html.EscapeString(category)),
			rows: buttons{{btn("Да, убрать", "bdy:"+key), btn("Отмена", "b:"+key)}},
		}, nil
	}
	if _, err := s.budgets.Delete(ctx, category); err != nil {
		return screen{}, err
	}
	sc, err := s.budgetsScreen(ctx)
	sc.toast = "Лимит убран"
	return sc, err
}

// ---- payments ----

func (s *Service) lastScreen(ctx context.Context) (screen, error) {
	list, err := s.txs.Last(ctx, 8)
	if err != nil {
		return screen{}, err
	}
	if len(list) == 0 {
		return screen{text: "🧾 <b>Последние траты</b>\n\nПока пусто.", rows: menuRow()}, nil
	}
	rows := buttons{}
	for _, t := range list {
		rows = append(rows, []models.InlineKeyboardButton{btn(
			fmt.Sprintf("%s %s · %s · %s", catEmoji(t.Category), ledgersvc.FormatAmount(t.AmountMinor), txTitle(t),
				t.TS.In(s.cfg.Location).Format("02.01")),
			fmt.Sprintf("t:%d", t.ID))})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("🏠 Меню", "m")})
	return screen{text: "🧾 <b>Последние траты</b>\n\nНажми на трату, чтобы сменить категорию или удалить.", rows: rows}, nil
}

func txTitle(t repo.Transaction) string {
	if t.Merchant != "" {
		return t.Merchant
	}
	if t.Name != "" {
		return t.Name
	}
	return "—"
}

func (s *Service) txScreen(ctx context.Context, id int64, toast string) (screen, error) {
	t, ok, err := s.txs.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.lastScreen(ctx)
	}
	return screen{toast: toast, text: ledgersvc.FormatSaved(t, s.cfg.Location), rows: buttons{
		{btn("🏷 Категория", fmt.Sprintf("tc:%d", id)), btn("🗑 Удалить", fmt.Sprintf("td:%d", id))},
		{btn("📝 Заметка", fmt.Sprintf("tn:%d", id)), btn("#️⃣ Теги", fmt.Sprintf("tt:%d", id))},
		{btn("‹ Последние", "ls"), btn("🏠 Меню", "m")},
	}}, nil
}

func (s *Service) txCategoryScreen(ctx context.Context, id int64) (screen, error) {
	t, ok, err := s.txs.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok {
		return s.lastScreen(ctx)
	}
	cats, err := s.knownCategories(ctx)
	if err != nil {
		return screen{}, err
	}
	rows := categoryButtons(cats, func(c string) string { return fmt.Sprintf("tk:%d:%s", id, catKey(c)) })
	rows = append(rows, []models.InlineKeyboardButton{btn("‹ Назад", fmt.Sprintf("t:%d", id))})

	text := "🏷 <b>Выбери категорию</b>"
	if t.Merchant != "" {
		text += fmt.Sprintf("\n\nЗапомню её для «%s», новые траты там попадут в неё сами.", html.EscapeString(t.Merchant))
	}
	return screen{text: text, rows: rows}, nil
}

func (s *Service) txSetCategory(ctx context.Context, id int64, key string) (screen, error) {
	category, ok, err := s.resolveCategory(ctx, key)
	if err != nil {
		return screen{}, err
	}
	t, found, err := s.txs.Get(ctx, id)
	if err != nil {
		return screen{}, err
	}
	if !ok || !found {
		return s.lastScreen(ctx)
	}
	if err := s.txs.UpdateCategory(ctx, id, category); err != nil {
		return screen{}, err
	}
	toast := "Категория обновлена"
	if t.Merchant != "" {
		if err := s.rules.Set(ctx, t.Merchant, category); err != nil {
			return screen{}, err
		}
		n, err := s.txs.CategorizeMerchant(ctx, t.Merchant, category, uncategorized)
		if err != nil {
			return screen{}, err
		}
		toast = "Запомнил для " + t.Merchant
		if n > 0 {
			toast += fmt.Sprintf(" (+%d прошлых)", n)
		}
	}
	return s.txScreen(ctx, id, toast)
}

func (s *Service) txDeleteScreen(ctx context.Context, id int64, confirmed bool) (screen, error) {
	if !confirmed {
		return screen{
			text: "🗑 Удалить эту трату?",
			rows: buttons{{btn("Да, удалить", fmt.Sprintf("tdy:%d", id)), btn("Отмена", fmt.Sprintf("t:%d", id))}},
		}, nil
	}
	if err := s.txs.Delete(ctx, id); err != nil {
		return screen{}, err
	}
	sc, err := s.lastScreen(ctx)
	sc.toast = "Удалено"
	return sc, err
}

// categoryButtons lays categories out two per row.
func categoryButtons(cats []string, route func(string) string) buttons {
	var rows buttons
	for i := 0; i < len(cats); i += 2 {
		row := []models.InlineKeyboardButton{btn(catEmoji(cats[i])+" "+cats[i], route(cats[i]))}
		if i+1 < len(cats) {
			row = append(row, btn(catEmoji(cats[i+1])+" "+cats[i+1], route(cats[i+1])))
		}
		rows = append(rows, row)
	}
	return rows
}
