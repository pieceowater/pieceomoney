package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type MerchantTotal struct {
	Merchant    string
	AmountMinor int64
	Count       int64
}

func (r *TransactionsRepo) Get(ctx context.Context, id int64) (Transaction, bool, error) {
	var t Transaction
	var ts int64
	err := r.db.QueryRowContext(ctx, `
		SELECT id, ts, amount_minor, currency, merchant, card, name, category
		FROM transactions WHERE id = ?`, id).
		Scan(&t.ID, &ts, &t.AmountMinor, &t.Currency, &t.Merchant, &t.Card, &t.Name, &t.Category)
	if errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, false, nil
	}
	t.TS = time.Unix(ts, 0)
	return t, err == nil, err
}

func (r *TransactionsRepo) UpdateCategory(ctx context.Context, id int64, category string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE transactions SET category = ? WHERE id = ?`, category, id)
	return err
}

func (r *TransactionsRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM transactions WHERE id = ?`, id)
	return err
}

// KnownCategories is every category name seen anywhere: payments, limits
// and merchant rules.
func (r *TransactionsRepo) KnownCategories(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT category FROM transactions WHERE category <> ''
		UNION SELECT category FROM budgets
		UNION SELECT category FROM merchant_rules
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TopMerchantsBetween ranks merchants inside one category.
func (r *TransactionsRepo) TopMerchantsBetween(ctx context.Context, category string, since, until time.Time, currency string, limit int) ([]MerchantTotal, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT CASE WHEN merchant = '' THEN name ELSE merchant END AS m, SUM(amount_minor), COUNT(*)
		FROM transactions
		WHERE category = ? COLLATE NOCASE AND ts >= ? AND ts < ? AND currency = ?
		GROUP BY m COLLATE NOCASE ORDER BY SUM(amount_minor) DESC LIMIT ?`,
		category, since.Unix(), until.Unix(), currency, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MerchantTotal
	for rows.Next() {
		var m MerchantTotal
		if err := rows.Scan(&m.Merchant, &m.AmountMinor, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// All returns every transaction, oldest first, for export.
func (r *TransactionsRepo) All(ctx context.Context) ([]Transaction, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, ts, amount_minor, currency, merchant, card, name, category
		FROM transactions ORDER BY ts, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Transaction
	for rows.Next() {
		var t Transaction
		var ts int64
		if err := rows.Scan(&t.ID, &ts, &t.AmountMinor, &t.Currency, &t.Merchant, &t.Card, &t.Name, &t.Category); err != nil {
			return nil, err
		}
		t.TS = time.Unix(ts, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// CategorizeMerchant gives every still-uncategorized payment of a merchant
// the chosen category. Payments the owner already categorized are left alone.
func (r *TransactionsRepo) CategorizeMerchant(ctx context.Context, merchant, category, placeholder string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE transactions SET category = ?
		WHERE merchant = ? COLLATE NOCASE AND (category = ? OR category = '')`, category, merchant, placeholder)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
