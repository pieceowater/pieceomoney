package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Goal struct {
	ID           int64
	Name         string
	TargetMinor  int64
	MonthlyMinor int64
	CreatedTS    time.Time
}

type GoalsRepo struct{ db *sql.DB }

func NewGoalsRepo(db *sql.DB) *GoalsRepo { return &GoalsRepo{db: db} }

func (r *GoalsRepo) Create(ctx context.Context, name string, target, monthly int64) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO goals (name, target_minor, monthly_minor, created_ts) VALUES (?, ?, ?, ?)`,
		name, target, monthly, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *GoalsRepo) Update(ctx context.Context, id int64, name string, target, monthly int64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE goals SET name = ?, target_minor = ?, monthly_minor = ? WHERE id = ?`, name, target, monthly, id)
	return err
}

func (r *GoalsRepo) Delete(ctx context.Context, id int64) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM goal_deposits WHERE goal_id = ?`, id); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM goals WHERE id = ?`, id)
	return err
}

func (r *GoalsRepo) Get(ctx context.Context, id int64) (Goal, bool, error) {
	var g Goal
	var created int64
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, target_minor, monthly_minor, created_ts FROM goals WHERE id = ?`, id).
		Scan(&g.ID, &g.Name, &g.TargetMinor, &g.MonthlyMinor, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Goal{}, false, nil
	}
	g.CreatedTS = time.Unix(created, 0)
	return g, err == nil, err
}

func (r *GoalsRepo) List(ctx context.Context) ([]Goal, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, target_minor, monthly_minor, created_ts FROM goals ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Goal
	for rows.Next() {
		var g Goal
		var created int64
		if err := rows.Scan(&g.ID, &g.Name, &g.TargetMinor, &g.MonthlyMinor, &created); err != nil {
			return nil, err
		}
		g.CreatedTS = time.Unix(created, 0)
		out = append(out, g)
	}
	return out, rows.Err()
}

// Deposit records money put into (or, if negative, taken out of) a goal.
func (r *GoalsRepo) Deposit(ctx context.Context, goalID, amountMinor int64) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO goal_deposits (goal_id, ts, amount_minor) VALUES (?, ?, ?)`, goalID, time.Now().Unix(), amountMinor)
	return err
}

// SavedSince sums a goal's deposits with ts >= since (zero time = all time).
func (r *GoalsRepo) SavedSince(ctx context.Context, goalID int64, since time.Time) (int64, error) {
	var sum sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT SUM(amount_minor) FROM goal_deposits WHERE goal_id = ? AND ts >= ?`, goalID, since.Unix()).Scan(&sum)
	return sum.Int64, err
}
