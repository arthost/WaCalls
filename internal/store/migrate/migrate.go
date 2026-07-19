package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// migrationsTable uses a service-specific name to avoid collisions with other
// systems (e.g. Rails) that share the same PostgreSQL database and already
// own a schema_migrations table with a different column type.
const migrationsTable = "wacalls_schema_migrations"

func Apply(ctx context.Context, db *sql.DB, migrations [][]string) error {
	createSQL := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (version INTEGER PRIMARY KEY)`,
		migrationsTable,
	)
	if _, err := db.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create %s: %w", migrationsTable, err)
	}
	var current int
	querySQL := fmt.Sprintf(`SELECT COALESCE(MAX(version), 0) FROM %s`, migrationsTable)
	if err := db.QueryRowContext(ctx, querySQL).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for v := current; v < len(migrations); v++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migration %d: begin: %w", v+1, err)
		}
		for _, stmt := range migrations[v] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", v+1, err)
			}
		}
		insertSQL := fmt.Sprintf(`INSERT INTO %s (version) VALUES (%d)`, migrationsTable, v+1)
		if _, err := tx.ExecContext(ctx, insertSQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: record version: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: commit: %w", v+1, err)
		}
	}
	return nil
}
