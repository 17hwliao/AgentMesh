package main

import (
	"agentmesh/internal/configenv"
	"agentmesh/internal/reservation"
	"agentmesh/internal/tenant"
	"agentmesh/migrations"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal("migration_failed")
	}
	log.Print("migration_complete")
}
func run() error {
	if err := configenv.LoadSecretFiles(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, db, err := reservation.OpenMySQLRepository(os.Getenv("AGENTMESH_MIGRATE_MYSQL_DSN"), nil)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = migrations.Apply(ctx, db); err != nil {
		return err
	}
	id, key := os.Getenv("AGENTMESH_BOOTSTRAP_TENANT_ID"), os.Getenv("AGENTMESH_BOOTSTRAP_API_KEY")
	var routes map[string][]string
	if id == "" || len(key) < 32 || json.Unmarshal([]byte(os.Getenv("AGENTMESH_BOOTSTRAP_MODEL_ROUTES")), &routes) != nil {
		return errors.New("bootstrap_invalid")
	}
	err = tenant.NewMySQLLifecycle(db).CreateTenant(ctx, tenant.Tenant{ID: id, Enabled: true, ModelRoutes: routes})
	if err != nil && !errors.Is(err, tenant.ErrTenantExists) {
		return err
	}
	hash := sha256.Sum256([]byte(key))
	keyID := hex.EncodeToString(hash[:18])
	var existing []byte
	err = db.QueryRowContext(ctx, "SELECT key_hash FROM api_keys WHERE key_prefix=?", key[:8]).Scan(&existing)
	if err == nil {
		if string(existing) != string(hash[:]) {
			return errors.New("bootstrap_key_conflict")
		}
		return nil
	}
	_, err = db.ExecContext(ctx, `INSERT INTO api_keys (key_id,tenant_id,key_prefix,key_hash,enabled,created_at) VALUES (?,?,?,?,TRUE,UTC_TIMESTAMP(6))`, keyID, id, key[:8], hash[:])
	return err
}
