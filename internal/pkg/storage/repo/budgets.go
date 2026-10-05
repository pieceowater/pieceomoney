package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type CategoryTotal struct {
	Category    string
	AmountMinor int64
	Count       int64
}

type Budget struct {
	Category   string
	LimitMinor int64
}

type Spend struct {
	TS          time.Time
	AmountMinor int64
}

// CategoryTotalsBetween sums since <= ts < until per category in one
// currency, biggest first.
func (r *TransactionsRepo) CategoryTotalsBetween(ctx context.Context, since, until time.Time, currency string) ([]CategoryTotal, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT category, SUM(amount_minor), COUNT(*)
		FROM transactions WHERE ts >= ? AND ts < ? AND currency = ?
		GROUP BY category COLLATE NOCASE ORDER BY SUM(amount_minor) DESC`, since.Unix(), until.Unix(), currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CategoryTotal
	for rows.Next() {
		var c CategoryTotal
		if err := rows.Scan(&c.Category, &c.AmountMinor, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *TransactionsRepo) CategorySpentSince(ctx context.Context, category string, since time.Time, currency string) (int64, error) {
	var sum sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT SUM(amount_minor) FROM transactions
		WHERE ts >= ? AND currency = ? AND category = ? COLLATE NOCASE`, since.Unix(), currency, category).Scan(&sum)
	return sum.Int64, err
}

// SpendsSince returns every payment in one currency since the cutoff, for
// time-of-day / weekday analytics done in Go (the zone lives in config).
func (r *TransactionsRepo) SpendsSince(ctx context.Context, since time.Time, currency string) ([]Spend, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT ts, amount_minor FROM transactions WHERE ts >= ? AND currency = ?`, since.Unix(), currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Spend
	for rows.Next() {
		var s Spend
		var ts int64
		if err := rows.Scan(&ts, &s.AmountMinor); err != nil {
			return nil, err
		}
		s.TS = time.Unix(ts, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}

type BudgetsRepo struct{ db *sql.DB }

func NewBudgetsRepo(db *sql.DB) *BudgetsRepo { return &BudgetsRepo{db: db} }

func (r *BudgetsRepo) Set(ctx context.Context, category string, limitMinor int64) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO budgets (category, limit_minor) VALUES (?, ?)
		ON CONFLICT(category) DO UPDATE SET limit_minor = excluded.limit_minor`, category, limitMinor)
	return err
}

func (r *BudgetsRepo) Delete(ctx context.Context, category string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM budgets WHERE category = ?`, category)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Get returns the limit for a category; ok=false when none is set.
func (r *BudgetsRepo) Get(ctx context.Context, category string) (Budget, bool, error) {
	var b Budget
	err := r.db.QueryRowContext(ctx, `SELECT category, limit_minor FROM budgets WHERE category = ?`, category).
		Scan(&b.Category, &b.LimitMinor)
	if errors.Is(err, sql.ErrNoRows) {
		return Budget{}, false, nil
	}
	return b, err == nil, err
}

func (r *BudgetsRepo) List(ctx context.Context) ([]Budget, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT category, limit_minor FROM budgets ORDER BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Budget
	for rows.Next() {
		var b Budget
		if err := rows.Scan(&b.Category, &b.LimitMinor); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type RulesRepo struct{ db *sql.DB }

func NewRulesRepo(db *sql.DB) *RulesRepo { return &RulesRepo{db: db} }

func (r *RulesRepo) Set(ctx context.Context, merchant, category string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO merchant_rules (merchant, category) VALUES (?, ?)
		ON CONFLICT(merchant) DO UPDATE SET category = excluded.category`, merchant, category)
	return err
}

// For returns the remembered category for a merchant, or "".
func (r *RulesRepo) For(ctx context.Context, merchant string) (string, error) {
	var c string
	err := r.db.QueryRowContext(ctx, `SELECT category FROM merchant_rules WHERE merchant = ?`, merchant).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return c, err
}
