package repo

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A database created by the first release (no category/note/tags columns,
// no goals tables) must open, keep its rows and gain the new columns.
func TestConnectMigratesOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`
		CREATE TABLE transactions (
		    id INTEGER PRIMARY KEY AUTOINCREMENT,
		    ts INTEGER NOT NULL, created_ts INTEGER NOT NULL, amount_minor INTEGER NOT NULL,
		    currency TEXT NOT NULL, merchant TEXT NOT NULL, card TEXT NOT NULL, name TEXT NOT NULL,
		    raw TEXT NOT NULL, fingerprint TEXT NOT NULL UNIQUE);
		INSERT INTO transactions (ts, created_ts, amount_minor, currency, merchant, card, name, raw, fingerprint)
		VALUES (1790000000, 1790000000, 35000, 'KZT', 'Love Is Coffee', 'Freedom', '', 'raw', 'fp1');`)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()

	db, err := Connect(path)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	repo := NewTransactionsRepo(db)
	list, err := repo.All(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("rows after migration: %d err=%v", len(list), err)
	}
	if list[0].Merchant != "Love Is Coffee" || list[0].AmountMinor != 35000 || list[0].Note != "" || list[0].Tags != "" {
		t.Errorf("unexpected row: %+v", list[0])
	}
	if err := repo.SetTags(context.Background(), list[0].ID, "a,b"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGoalsRepo(db).Create(context.Background(), "g", 100, 10); err != nil {
		t.Fatalf("goals table missing after migration: %v", err)
	}

	// Connecting again must be a no-op.
	db.Close()
	if _, err := Connect(path); err != nil {
		t.Fatalf("second connect: %v", err)
	}
}
