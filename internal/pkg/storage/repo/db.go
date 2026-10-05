// Package repo is the sqlite-backed persistence layer: schema + one small
// repo type per table.
package repo

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS transactions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts INTEGER NOT NULL,            -- when the payment happened (unix)
    created_ts INTEGER NOT NULL,    -- when we stored it (unix)
    amount_minor INTEGER NOT NULL,  -- 2500.50 -> 250050, signed (refunds < 0)
    currency TEXT NOT NULL,
    merchant TEXT NOT NULL,
    card TEXT NOT NULL,
    name TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '',   -- comma-separated, lowercase
    raw TEXT NOT NULL,
    -- sha256 of the normalized fields: an Apple Shortcuts automation can
    -- fire twice for one tap, and the same payload must not count twice.
    fingerprint TEXT NOT NULL UNIQUE
);
CREATE INDEX IF NOT EXISTS idx_transactions_ts ON transactions(ts);

-- Monthly limit per category, in the default currency.
CREATE TABLE IF NOT EXISTS budgets (
    category TEXT PRIMARY KEY COLLATE NOCASE,
    limit_minor INTEGER NOT NULL
);

-- Savings goals; deposits are tracked separately so progress can be shown
-- for the current month as well as in total.
CREATE TABLE IF NOT EXISTS goals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    target_minor INTEGER NOT NULL,
    monthly_minor INTEGER NOT NULL DEFAULT 0,
    created_ts INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS goal_deposits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    goal_id INTEGER NOT NULL,
    ts INTEGER NOT NULL,
    amount_minor INTEGER NOT NULL   -- negative = withdrawal
);
CREATE INDEX IF NOT EXISTS idx_goal_deposits_goal ON goal_deposits(goal_id, ts);

-- merchant -> category, for payments whose payload carries no category.
CREATE TABLE IF NOT EXISTS merchant_rules (
    merchant TEXT PRIMARY KEY COLLATE NOCASE,
    category TEXT NOT NULL
);
`

func Connect(dbPath string) (*sql.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("repo: create data dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("repo: open: %w", err)
	}
	// SQLite allows only one writer at a time anyway, and the write volume
	// never justifies a real pool.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("repo: set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, fmt.Errorf("repo: set busy_timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("repo: apply schema: %w", err)
	}
	// Columns added after the first release of the schema; no-ops on a table
	// that already has them.
	for _, col := range []string{
		"category TEXT NOT NULL DEFAULT ''",
		"note TEXT NOT NULL DEFAULT ''",
		"tags TEXT NOT NULL DEFAULT ''",
	} {
		if _, err := db.Exec("ALTER TABLE transactions ADD COLUMN " + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			return nil, fmt.Errorf("repo: migrate transactions (%s): %w", col, err)
		}
	}
	return db, nil
}
