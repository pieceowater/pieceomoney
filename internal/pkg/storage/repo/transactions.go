package repo

import (
	"context"
	"database/sql"
	"time"
)

type Transaction struct {
	ID          int64
	TS          time.Time
	AmountMinor int64
	Currency    string
	Merchant    string
	Card        string
	Name        string
	Category    string
	Note        string
	Tags        string // comma-separated, lowercase
	Raw         string
	Fingerprint string
}

type CurrencyTotal struct {
	Currency    string
	AmountMinor int64
	Count       int64
}

type TransactionsRepo struct{ db *sql.DB }

func NewTransactionsRepo(db *sql.DB) *TransactionsRepo { return &TransactionsRepo{db: db} }

const txColumns = "id, ts, amount_minor, currency, merchant, card, name, category, note, tags"

func scanTransactions(rows *sql.Rows) ([]Transaction, error) {
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		var t Transaction
		var ts int64
		if err := rows.Scan(&t.ID, &ts, &t.AmountMinor, &t.Currency, &t.Merchant, &t.Card, &t.Name, &t.Category, &t.Note, &t.Tags); err != nil {
			return nil, err
		}
		t.TS = time.Unix(ts, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Insert stores t and reports its id and whether it was new -- false means a
// row with the same fingerprint already existed (a duplicate Shortcut firing).
func (r *TransactionsRepo) Insert(ctx context.Context, t Transaction) (int64, bool, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO transactions
		    (ts, created_ts, amount_minor, currency, merchant, card, name, category, note, tags, raw, fingerprint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TS.Unix(), time.Now().Unix(), t.AmountMinor, t.Currency, t.Merchant, t.Card, t.Name, t.Category, t.Note, t.Tags, t.Raw, t.Fingerprint)
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	id, err := res.LastInsertId()
	return id, err == nil, err
}

func (r *TransactionsRepo) Last(ctx context.Context, limit int) ([]Transaction, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+txColumns+` FROM transactions ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanTransactions(rows)
}

// Between returns payments with since <= ts < until, oldest first. Personal
// volumes are small, so grouping and searching happen in Go (SQLite's
// LIKE/lower() are ASCII-only, which breaks Cyrillic matching).
func (r *TransactionsRepo) Between(ctx context.Context, since, until time.Time) ([]Transaction, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+txColumns+` FROM transactions WHERE ts >= ? AND ts < ? ORDER BY ts, id`,
		since.Unix(), until.Unix())
	if err != nil {
		return nil, err
	}
	return scanTransactions(rows)
}

// All returns every transaction, oldest first.
func (r *TransactionsRepo) All(ctx context.Context) ([]Transaction, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+txColumns+` FROM transactions ORDER BY ts, id`)
	if err != nil {
		return nil, err
	}
	return scanTransactions(rows)
}

// TotalsBetween sums transactions with since <= ts < until, per currency.
func (r *TransactionsRepo) TotalsBetween(ctx context.Context, since, until time.Time) ([]CurrencyTotal, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT currency, SUM(amount_minor), COUNT(*)
		FROM transactions WHERE ts >= ? AND ts < ?
		GROUP BY currency ORDER BY SUM(amount_minor) DESC`, since.Unix(), until.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CurrencyTotal
	for rows.Next() {
		var c CurrencyTotal
		if err := rows.Scan(&c.Currency, &c.AmountMinor, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
