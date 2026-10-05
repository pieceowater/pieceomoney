// Package svc holds the pure analytics over lists of transactions:
// groupings, search, recurring-payment detection and the "unusually large
// payment" threshold. No I/O, so everything is unit-testable.
package svc

import (
	"sort"
	"strings"
	"time"

	"pieceomoney/internal/pkg/storage/repo"
)

type Group struct {
	Key         string
	AmountMinor int64
	Count       int64
}

// GroupBy sums payments in one currency under every key returned by keyFn
// (a payment can land in several groups, e.g. one per tag). Keys that differ
// only by case are merged and shown as first seen. Biggest first.
func GroupBy(txs []repo.Transaction, currency string, keyFn func(repo.Transaction) []string) []Group {
	idx := map[string]*Group{}
	var order []string
	for _, t := range txs {
		if t.Currency != currency {
			continue
		}
		for _, k := range keyFn(t) {
			id := strings.ToLower(k)
			g, ok := idx[id]
			if !ok {
				g = &Group{Key: k}
				idx[id] = g
				order = append(order, id)
			}
			g.AmountMinor += t.AmountMinor
			g.Count++
		}
	}
	out := make([]Group, 0, len(order))
	for _, id := range order {
		out = append(out, *idx[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AmountMinor > out[j].AmountMinor })
	return out
}

func ByCard(txs []repo.Transaction, currency string) []Group {
	return GroupBy(txs, currency, func(t repo.Transaction) []string { return []string{orDash(t.Card)} })
}

func ByMerchant(txs []repo.Transaction, currency string) []Group {
	return GroupBy(txs, currency, func(t repo.Transaction) []string {
		if t.Merchant != "" {
			return []string{t.Merchant}
		}
		return []string{orDash(t.Name)}
	})
}

func ByTag(txs []repo.Transaction, currency string) []Group {
	return GroupBy(txs, currency, func(t repo.Transaction) []string { return SplitTags(t.Tags) })
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// SplitTags parses the stored comma-separated tag list.
func SplitTags(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// NormalizeTags turns free user input ("#Trip, gift  work") into the stored
// form: lowercase, no '#', deduplicated, comma-separated.
func NormalizeTags(input string) string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' }) {
		f = strings.ToLower(strings.TrimLeft(strings.TrimSpace(f), "#"))
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return strings.Join(out, ",")
}

// DayTotals returns one total per calendar day starting at the day of
// `start`, for `days` days, in one currency.
func DayTotals(txs []repo.Transaction, currency string, loc *time.Location, start time.Time, days int) []int64 {
	first := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	out := make([]int64, days)
	for _, t := range txs {
		if t.Currency != currency {
			continue
		}
		d := t.TS.In(loc)
		day := int(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).Sub(first).Hours()/24 + 0.5)
		if day >= 0 && day < days {
			out[day] += t.AmountMinor
		}
	}
	return out
}

type MonthTotal struct {
	Start       time.Time
	AmountMinor int64
}

// MonthTotals returns the last n months (oldest first, ending with the month
// of `now`) in one currency.
func MonthTotals(txs []repo.Transaction, currency string, loc *time.Location, now time.Time, n int) []MonthTotal {
	n0 := now.In(loc)
	cur := time.Date(n0.Year(), n0.Month(), 1, 0, 0, 0, 0, loc)
	out := make([]MonthTotal, n)
	for i := range out {
		out[i].Start = cur.AddDate(0, -(n - 1 - i), 0)
	}
	for _, t := range txs {
		if t.Currency != currency {
			continue
		}
		d := t.TS.In(loc)
		m := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, loc)
		for i := range out {
			if out[i].Start.Equal(m) {
				out[i].AmountMinor += t.AmountMinor
			}
		}
	}
	return out
}

// Search matches payments by free text across merchant, name, note,
// category and tags (case-insensitive, Unicode-aware). A query starting
// with '#' matches tags only.
func Search(txs []repo.Transaction, query string) []repo.Transaction {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	tagOnly := strings.HasPrefix(q, "#")
	q = strings.TrimLeft(q, "#")

	var out []repo.Transaction
	for _, t := range txs {
		if tagOnly {
			for _, tag := range SplitTags(t.Tags) {
				if strings.Contains(tag, q) {
					out = append(out, t)
					break
				}
			}
			continue
		}
		hay := strings.ToLower(strings.Join([]string{t.Merchant, t.Name, t.Note, t.Category, t.Tags}, "\n"))
		if strings.Contains(hay, q) {
			out = append(out, t)
		}
	}
	return out
}

type Recurring struct {
	Merchant    string
	Currency    string
	AmountMinor int64 // typical (median) amount
	Every       time.Duration
	Label       string // "weekly" / "monthly"
	Last        time.Time
	Next        time.Time
	Count       int
}

// DetectRecurring finds payments that repeat on a steady schedule with a
// similar amount: weekly (4+ payments) or monthly (3+ payments) per
// merchant. A series whose next expected date is long past is considered
// cancelled and dropped. Result is ordered by next expected date.
func DetectRecurring(txs []repo.Transaction, now time.Time) []Recurring {
	type key struct{ merchant, currency string }
	groups := map[key][]repo.Transaction{}
	names := map[key]string{}
	for _, t := range txs {
		if t.Merchant == "" || t.AmountMinor <= 0 {
			continue
		}
		k := key{strings.ToLower(t.Merchant), t.Currency}
		groups[k] = append(groups[k], t)
		if _, ok := names[k]; !ok {
			names[k] = t.Merchant
		}
	}

	var out []Recurring
	for k, list := range groups {
		sort.Slice(list, func(i, j int) bool { return list[i].TS.Before(list[j].TS) })
		if len(list) < 3 {
			continue
		}

		amounts := make([]int64, len(list))
		for i, t := range list {
			amounts[i] = t.AmountMinor
		}
		medAmount := median(amounts)

		var gaps []float64
		for i := 1; i < len(list); i++ {
			gaps = append(gaps, list[i].TS.Sub(list[i-1].TS).Hours()/24)
		}
		medGap := medianF(gaps)

		var label string
		var tolerance float64
		switch {
		case medGap >= 6 && medGap <= 8 && len(list) >= 4:
			label, tolerance = "weekly", 2
		case medGap >= 26 && medGap <= 34:
			label, tolerance = "monthly", 5
		default:
			continue
		}

		if share(gaps, func(g float64) bool { return abs(g-medGap) <= tolerance }) < 0.7 {
			continue
		}
		if share(floats(amounts), func(a float64) bool { return abs(a-float64(medAmount)) <= 0.2*float64(medAmount) }) < 0.7 {
			continue
		}

		every := time.Duration(medGap * 24 * float64(time.Hour))
		last := list[len(list)-1].TS
		next := last.Add(every)
		if now.Sub(next) > 2*every {
			continue
		}
		out = append(out, Recurring{
			Merchant: names[k], Currency: k.currency, AmountMinor: medAmount,
			Every: every, Label: label, Last: last, Next: next, Count: len(list),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Next.Before(out[j].Next) })
	return out
}

// MonthlyCost approximates what a recurring payment costs per month.
func (r Recurring) MonthlyCost() int64 {
	if r.Label == "weekly" {
		return r.AmountMinor * 433 / 100
	}
	return r.AmountMinor
}

// UnusualThreshold returns the amount above which a payment is "unusually
// large" given recent payment amounts: the larger of mult x median and 1.5 x
// the 90th percentile, so habitual big purchases don't trigger it. ok is
// false while there's too little history to judge.
func UnusualThreshold(amounts []int64, mult float64) (int64, bool) {
	const minHistory = 15
	if len(amounts) < minHistory {
		return 0, false
	}
	sorted := append([]int64(nil), amounts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	med := median(sorted)
	p90 := sorted[len(sorted)*9/10]
	thr := int64(mult * float64(med))
	if alt := p90 * 3 / 2; alt > thr {
		thr = alt
	}
	return thr, thr > 0
}

func median(v []int64) int64 {
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func medianF(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func share(v []float64, ok func(float64) bool) float64 {
	if len(v) == 0 {
		return 0
	}
	n := 0
	for _, x := range v {
		if ok(x) {
			n++
		}
	}
	return float64(n) / float64(len(v))
}

func floats(v []int64) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
