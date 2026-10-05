package repo

import (
	"context"
	"time"
)

type MerchantTotal struct {
	Merchant    string
	AmountMinor int64
	Count       int64
}

func (r *TransactionsRepo) Get(ctx context.Context, id int64) (Transaction, bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+txColumns+` FROM transactions WHERE id = ?`, id)
	if err != nil {
		return Transaction{}, false, err
	}
	list, err := scanTransactions(rows)
	if err != nil || len(list) == 0 {
		return Transaction{}, false, err
	}
	return list[0], true, nil
}

func (r *TransactionsRepo) SetNote(ctx context.Context, id int64, note string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE transactions SET note = ? WHERE id = ?`, note, id)
	return err
}

func (r *TransactionsRepo) SetTags(ctx context.Context, id int64, tags string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE transactions SET tags = ? WHERE id = ?`, tags, id)
	return err
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

// MerchantPayments counts a merchant's payments other than excludeID
// (case-insensitive name match); used to detect a first purchase there.
func (r *TransactionsRepo) MerchantPayments(ctx context.Context, merchant string, excludeID int64) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transactions WHERE merchant = ? COLLATE NOCASE AND id <> ?`, merchant, excludeID).Scan(&n)
	return n, err
}
