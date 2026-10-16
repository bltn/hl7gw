package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strings"
)

type migration struct {
	name string
	sql  string
}

//go:embed migrations/*.sql
var migrationFS embed.FS

var migrations = loadMigrations()

func loadMigrations() []migration {
	entries, _ := migrationFS.ReadDir("migrations")
	migs := make([]migration, len(entries))
	for i, e := range entries {
		sql, _ := migrationFS.ReadFile("migrations/" + e.Name())
		migs[i] = migration{name: strings.TrimSuffix(e.Name(), ".sql"), sql: string(sql)}
	}
	return migs
}

func migrate(ctx context.Context, db *sql.DB, migs []migration) error {
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("reading user_version: %w", err)
	}
	if v > len(migs) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", v, len(migs))
	}

	for ; v < len(migs); v++ {
		if err := apply(ctx, db, migs[v], v+1); err != nil {
			return fmt.Errorf("migration %s: %w", migs[v].name, err)
		}
	}

	return nil
}

func apply(ctx context.Context, db *sql.DB, m migration, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("%s;\nPRAGMA user_version = %d", m.sql, version)); err != nil {
		return err
	}

	return tx.Commit()
}
