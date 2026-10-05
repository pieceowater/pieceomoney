// Package svc turns the JSON text the iOS Shortcut sends into a normalized
// transaction (Parse) and renders transactions back as chat messages
// (FormatSaved, FormatAmount). No I/O -- storage and Telegram live elsewhere.
package svc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"pieceomoney/internal/pkg/storage/repo"
)

// payload is the contract from the Shortcut. Amount is raw because Shortcuts
// happily sends either 2500 or "2 500,00 ₸" depending on how the field was
// wired up.
type payload struct {
	Amount   json.RawMessage `json:"amount"`
	Currency string          `json:"currency"`
	Merchant string          `json:"merchant"`
	Card     string          `json:"card"`
	Name     string          `json:"name"`
	Category string          `json:"category"`
	Date     string          `json:"date"`
}

var dateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"02.01.2006 15:04:05",
	"02.01.2006 15:04",
	"02.01.2006",
}

// Parse extracts a transaction from a message text. loc is used for dates
// without a zone and as the fallback timestamp's zone.
//
// Two formats are accepted: a JSON object, or a compact pipe-separated line
// "amount|merchant|card|category[|currency[|date]]". The pipe form exists
// because Shortcuts can't escape quotes, and merchant names like Acme "Corp" Ltd
// would break hand-built JSON.
func Parse(text, defaultCurrency string, loc *time.Location, now time.Time) (repo.Transaction, error) {
	var p payload
	var raw string
	if strings.HasPrefix(strings.TrimSpace(text), "{") || strings.Contains(text, "```") {
		body, err := extractJSON(text)
		if err != nil {
			return repo.Transaction{}, err
		}
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return repo.Transaction{}, fmt.Errorf("битый JSON: %v", err)
		}
		raw = body
	} else {
		var err error
		if p, err = parsePipe(text); err != nil {
			return repo.Transaction{}, err
		}
		raw = strings.TrimSpace(text)
	}
	if len(p.Amount) == 0 || string(p.Amount) == "null" {
		return repo.Transaction{}, errors.New("нет поля amount")
	}

	amount, err := parseAmount(strings.Trim(string(p.Amount), `"`))
	if err != nil {
		return repo.Transaction{}, err
	}

	ts := now
	hasDate := strings.TrimSpace(p.Date) != ""
	if hasDate {
		ts, err = parseDate(strings.TrimSpace(p.Date), loc)
		if err != nil {
			return repo.Transaction{}, err
		}
	}

	currency := strings.ToUpper(strings.TrimSpace(p.Currency))
	if currency == "" {
		currency = detectCurrency(strings.Trim(string(p.Amount), `"`))
	}
	if currency == "" {
		currency = defaultCurrency
	}

	t := repo.Transaction{
		TS:          ts,
		AmountMinor: amount,
		Currency:    currency,
		Merchant:    strings.TrimSpace(p.Merchant),
		Card:        strings.TrimSpace(p.Card),
		Name:        strings.TrimSpace(p.Name),
		Category:    strings.TrimSpace(p.Category),
		Raw:         raw,
	}
	// Without an explicit date the timestamp is "now", which differs between
	// two firings of the same automation; bucket it so they dedupe.
	bucket := ts.Unix()
	if !hasDate {
		bucket = ts.Unix() / 60
	}
	t.Fingerprint = fingerprint(t, bucket)
	return t, nil
}

var knownCurrencies = map[string]bool{"KZT": true, "USD": true, "EUR": true, "RUB": true, "GBP": true, "UAH": true, "TRY": true, "CNY": true, "JPY": true, "AED": true}

// ParseManual reads a payment typed by hand: the amount first (no spaces
// inside it), then any of, in any order, a currency code (USD), a date
// (сегодня/today, вчера/yesterday, позавчера, 05.10 or 05.10.2026), #tags, and the rest is
// the merchant: "1500 Такси вчера #работа". Past dates are stamped at noon.
func ParseManual(text, defaultCurrency string, loc *time.Location, now time.Time) (repo.Transaction, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return repo.Transaction{}, errors.New("пусто: жду сумму и название, например 1500 Такси")
	}
	amount, err := parseAmount(fields[0])
	if err != nil {
		return repo.Transaction{}, fmt.Errorf("первым должна идти сумма без пробелов, например 1500 Такси (не понял %q)", fields[0])
	}

	currency := detectCurrency(fields[0])
	ts := now
	var tags, merchant []string
	for _, f := range fields[1:] {
		switch {
		case strings.HasPrefix(f, "#"):
			tags = append(tags, f)
		case knownCurrencies[strings.ToUpper(f)] && f == strings.ToUpper(f):
			currency = f
		default:
			if d, ok := parseRelativeDate(f, now, loc); ok {
				ts = d
				continue
			}
			merchant = append(merchant, f)
		}
	}
	if len(merchant) == 0 {
		return repo.Transaction{}, errors.New("добавь название магазина после суммы, например 1500 Такси")
	}
	if currency == "" {
		currency = defaultCurrency
	}

	t := repo.Transaction{
		TS: ts, AmountMinor: amount, Currency: currency,
		Merchant: strings.Join(merchant, " "), Card: "Вручную",
		Tags: normalizeTags(strings.Join(tags, " ")), Raw: strings.TrimSpace(text),
	}
	// Manual entries are never deduplicated: adding the same coffee twice is
	// legitimate, so the fingerprint carries a unique nonce.
	t.Fingerprint = fingerprint(t, now.UnixNano())
	return t, nil
}

// parseRelativeDate understands the date words ParseManual accepts.
func parseRelativeDate(f string, now time.Time, loc *time.Location) (time.Time, bool) {
	local := now.In(loc)
	noon := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, loc) }
	switch strings.ToLower(f) {
	case "сегодня", "today":
		return now, true
	case "вчера", "yesterday":
		return noon(local.AddDate(0, 0, -1)), true
	case "позавчера":
		return noon(local.AddDate(0, 0, -2)), true
	}
	for _, layout := range []string{"02.01.2006", "02.01.06"} {
		if t, err := time.ParseInLocation(layout, f, loc); err == nil {
			return noon(t), true
		}
	}
	if t, err := time.ParseInLocation("02.01", f, loc); err == nil {
		d := time.Date(local.Year(), t.Month(), t.Day(), 12, 0, 0, 0, loc)
		if d.After(local.AddDate(0, 0, 1)) {
			d = d.AddDate(-1, 0, 0) // "05.12" typed in October means last December
		}
		return d, true
	}
	return time.Time{}, false
}

// normalizeTags mirrors analytics.NormalizeTags (kept local so the ledger
// package has no dependency on analytics).
func normalizeTags(input string) string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		f = strings.ToLower(strings.TrimLeft(strings.TrimSpace(f), "#"))
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return strings.Join(out, ",")
}

func parsePipe(text string) (payload, error) {
	parts := strings.Split(strings.TrimSpace(text), "|")
	if len(parts) < 2 {
		return payload{}, errors.New("не понял формат: жду JSON или строку вида сумма|магазин|карта|категория")
	}
	field := func(i int) string {
		if i < len(parts) {
			return strings.TrimSpace(parts[i])
		}
		return ""
	}
	amount, _ := json.Marshal(field(0))
	return payload{
		Amount:   amount,
		Merchant: field(1),
		Card:     field(2),
		Category: field(3),
		Currency: field(4),
		Date:     field(5),
	}, nil
}

// extractJSON tolerates a Shortcut (or a human pasting) wrapping the object
// in a code fence or surrounding text.
func extractJSON(text string) (string, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return "", errors.New("в сообщении нет JSON-объекта")
	}
	return text[start : end+1], nil
}

var currencySymbols = map[rune]string{'₸': "KZT", '$': "USD", '€': "EUR", '₽': "RUB", '£': "GBP", '₴': "UAH", '¥': "JPY"}

// detectCurrency finds a currency in an amount string: Wallet sends
// "KZT 350.00", other sources use a symbol like "₸2,500".
func detectCurrency(amount string) string {
	run := 0
	for i, r := range amount {
		if r >= 'A' && r <= 'Z' {
			run++
			if run == 3 {
				return amount[i-2 : i+1]
			}
			continue
		}
		run = 0
		if code, ok := currencySymbols[r]; ok {
			return code
		}
	}
	return ""
}

func parseDate(s string, loc *time.Location) (time.Time, error) {
	for _, layout := range dateLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("не понял дату %q", s)
}

// parseAmount returns minor units (cents). Handles "2500", "2500.5",
// "2 500,00", "1,234.56", "1.234,56", and a leading minus; currency symbols
// and spaces are ignored.
func parseAmount(s string) (int64, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '.', r == ',':
			b.WriteRune(r)
		case r == '-' || r == '−' || r == '–':
			if b.Len() == 0 {
				b.WriteByte('-')
			}
		}
	}
	str := b.String()
	neg := strings.HasPrefix(str, "-")
	str = strings.TrimPrefix(str, "-")
	if str == "" {
		return 0, fmt.Errorf("не понял сумму %q", s)
	}

	// The last separator is the decimal one if it's followed by 1-2 digits;
	// otherwise every separator is a thousands separator.
	frac := ""
	if i := strings.LastIndexAny(str, ".,"); i >= 0 {
		if tail := str[i+1:]; len(tail) >= 1 && len(tail) <= 2 {
			frac = tail
			str = str[:i]
		}
	}
	whole := strings.NewReplacer(".", "", ",", "").Replace(str)
	if whole == "" {
		whole = "0"
	}
	for len(frac) < 2 {
		frac += "0"
	}

	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("не понял сумму %q", s)
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	v := w*100 + f
	if neg {
		v = -v
	}
	return v, nil
}

func fingerprint(t repo.Transaction, bucket int64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s|%s|%d",
		t.AmountMinor, t.Currency, t.Merchant, t.Card, t.Name, bucket)))
	return hex.EncodeToString(h[:])
}

// FormatAmount renders minor units as "2,500" / "2,500.50".
func FormatAmount(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	whole := strconv.FormatInt(minor/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := sign + b.String()
	if c := minor % 100; c != 0 {
		out += fmt.Sprintf(".%02d", c)
	}
	return out
}

// FormatSaved is the HTML confirmation sent after a transaction is stored.
func FormatSaved(t repo.Transaction, loc *time.Location) string {
	title := "Расход записан"
	if t.AmountMinor < 0 {
		title = "Возврат записан"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "💳 <b>%s:</b> %s %s", title, FormatAmount(abs(t.AmountMinor)), html.EscapeString(t.Currency))
	if t.Merchant != "" {
		fmt.Fprintf(&b, "\n🏪 <b>Магазин:</b> %s", html.EscapeString(t.Merchant))
	}
	if t.Card != "" {
		fmt.Fprintf(&b, "\n💳 <b>Карта:</b> %s", html.EscapeString(t.Card))
	}
	if t.Category != "" {
		fmt.Fprintf(&b, "\n🏷 <b>Категория:</b> %s", html.EscapeString(t.Category))
	}
	if t.Name != "" && t.Name != t.Merchant {
		fmt.Fprintf(&b, "\n🧾 <b>Описание:</b> %s", html.EscapeString(t.Name))
	}
	fmt.Fprintf(&b, "\n📅 <b>Дата:</b> %s", t.TS.In(loc).Format("02.01.2006 15:04"))
	if t.Note != "" {
		fmt.Fprintf(&b, "\n📝 %s", html.EscapeString(t.Note))
	}
	if t.Tags != "" {
		tags := strings.Split(t.Tags, ",")
		for i, tag := range tags {
			tags[i] = "#" + html.EscapeString(tag)
		}
		fmt.Fprintf(&b, "\n%s", strings.Join(tags, " "))
	}
	return b.String()
}

// ParseAmount is the exported amount parser for command arguments.
func ParseAmount(s string) (int64, error) { return parseAmount(s) }

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
