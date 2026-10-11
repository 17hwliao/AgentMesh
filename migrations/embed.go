package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Apply records each DDL step separately: MySQL DDL is not transactionally
// grouped with the migration journal. IF NOT EXISTS makes crash retries safe.
func Apply(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('agentmesh-migrations',30)").Scan(&locked); err != nil || !locked.Valid || locked.Int64 != 1 {
		return errors.New("migration_lock_failed")
	}
	defer func() {
		var released sql.NullInt64
		_ = conn.QueryRowContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK('agentmesh-migrations')").Scan(&released)
	}()
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agentmesh_migration_steps (step VARCHAR(160) PRIMARY KEY,sha256 CHAR(64) NOT NULL,applied_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6))`); err != nil {
		return err
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		raw, _ := files.ReadFile(name)
		var lines []string
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				lines = append(lines, line)
			}
		}
		statements := strings.Split(strings.Join(lines, "\n"), ";")
		for index, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			step := fmt.Sprintf("%s:%d", name, index)
			sum := sha256.Sum256([]byte(stmt))
			hash := hex.EncodeToString(sum[:])
			var previous string
			err = conn.QueryRowContext(ctx, "SELECT sha256 FROM agentmesh_migration_steps WHERE step=?", step).Scan(&previous)
			if err == nil {
				if previous != hash {
					return errors.New("migration_checksum_mismatch")
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if name == "008_third_provider.sql" {
				constraint := "chk_tenant_model_routes_provider"
				if strings.Contains(stmt, "ADD CONSTRAINT") {
					constraint += "_v2"
				}
				var count int
				if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() AND TABLE_NAME='tenant_model_routes' AND CONSTRAINT_NAME=?", constraint).Scan(&count); err != nil {
					return err
				}
				if (strings.Contains(stmt, "DROP CHECK") && count > 0) || (strings.Contains(stmt, "ADD CONSTRAINT") && count == 0) {
					if _, err = conn.ExecContext(ctx, stmt); err != nil {
						return fmt.Errorf("migration %s failed", step)
					}
				}
			} else if name == "005_usage_kafka_outbox_leases.sql" {
				for _, field := range []struct{ name, ddl string }{
					{"lease_owner", "ADD COLUMN lease_owner VARCHAR(128) NULL AFTER available_at"},
					{"lease_until", "ADD COLUMN lease_until DATETIME(6) NULL AFTER lease_owner"},
				} {
					var count int
					if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='usage_kafka_outbox' AND COLUMN_NAME=?", field.name).Scan(&count); err != nil {
						return err
					}
					if count == 0 {
						if _, err = conn.ExecContext(ctx, "ALTER TABLE usage_kafka_outbox "+field.ddl); err != nil {
							return err
						}
					}
				}
				var count int
				if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='usage_kafka_outbox' AND INDEX_NAME='ix_usage_kafka_outbox_claim'").Scan(&count); err != nil {
					return err
				}
				if count == 0 {
					if _, err = conn.ExecContext(ctx, "ALTER TABLE usage_kafka_outbox ADD KEY ix_usage_kafka_outbox_claim (published_at,available_at,lease_until,created_at)"); err != nil {
						return err
					}
				}
			} else {
				stmt = strings.Replace(stmt, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1)
				stmt = strings.Replace(stmt, "IF NOT EXISTS IF NOT EXISTS ", "IF NOT EXISTS ", 1)
				if _, err = conn.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("migration %s failed", step)
				}
			}
			if _, err = conn.ExecContext(ctx, "INSERT INTO agentmesh_migration_steps (step,sha256) VALUES (?,?)", step, hash); err != nil {
				return err
			}
		}
	}
	return nil
}
