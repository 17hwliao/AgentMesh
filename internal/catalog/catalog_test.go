package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestOpenAndMigrateAreRepeatable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	for attempt := 1; attempt <= 2; attempt++ {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() attempt %d error = %v", attempt, err)
		}
	}

	generation, err := store.GetMeta(ctx, MetaGeneration)
	if err != nil {
		t.Fatalf("GetMeta(%q) error = %v", MetaGeneration, err)
	}
	if generation != "0" {
		t.Fatalf("initial generation = %q, want %q", generation, "0")
	}

	var schemaRows, version int
	if err := store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MIN(version), 0) FROM schema_version`).Scan(&schemaRows, &version); err != nil {
		t.Fatalf("read schema_version error = %v", err)
	}
	if schemaRows != 1 || version != 1 {
		t.Fatalf("schema_version rows = %d, version = %d; want 1, 1", schemaRows, version)
	}

	tables := []string{
		"schema_version",
		"catalog_metadata",
		"providers",
		"provider_credentials",
		"models",
		"client_keys",
		"routing_groups",
		"routing_members",
		"usage_events",
		"cooldowns",
		"audit_log",
	}
	for _, table := range tables {
		var name string
		err := store.DB.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			t.Errorf("table %q is missing", table)
			continue
		}
		if err != nil {
			t.Errorf("look up table %q error = %v", table, err)
			continue
		}
		if name != table {
			t.Errorf("table lookup returned %q, want %q", name, table)
		}
	}

	if err := store.SetMeta(ctx, MetaGeneration, "9"); err != nil {
		t.Fatalf("SetMeta(%q) error = %v", MetaGeneration, err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() after metadata update error = %v", err)
	}
	generation, err = store.GetMeta(ctx, MetaGeneration)
	if err != nil {
		t.Fatalf("GetMeta(%q) after remigrate error = %v", MetaGeneration, err)
	}
	if generation != "9" {
		t.Fatalf("generation after remigrate = %q, want %q", generation, "9")
	}
}

func TestProviderLifecycle(t *testing.T) {
	ctx := context.Background()
	store := openCatalogTestStore(t)
	createdAt := catalogTestTime()
	provider := Provider{
		ID:          "prv_acme",
		Slug:        "acme",
		Name:        "Acme",
		BaseURL:     "https://api.acme.example/v1",
		Kind:        ProviderOpenAICompatible,
		AuthType:    AuthBearer,
		TLSInsecure: true,
		Enabled:     true,
		Revision:    1,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}

	runCatalogTx(t, store, func(tx *sql.Tx) error {
		return InsertProvider(ctx, tx, provider)
	})

	snapshot := loadCatalogSnapshot(t, store)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers after insert = %d, want 1", len(snapshot.Providers))
	}
	got := snapshot.Providers[0]
	if got.ID != provider.ID || got.Slug != provider.Slug || got.Name != provider.Name ||
		got.BaseURL != provider.BaseURL || got.Kind != provider.Kind || got.AuthType != provider.AuthType ||
		got.TLSInsecure != provider.TLSInsecure ||
		got.Enabled != provider.Enabled || got.Revision != provider.Revision {
		t.Fatalf("provider after insert = %+v, want %+v", got, provider)
	}
	requireTimeEqual(t, "created_at", got.CreatedAt, provider.CreatedAt)
	requireTimeEqual(t, "updated_at", got.UpdatedAt, provider.UpdatedAt)

	provider.Name = "Acme Updated"
	provider.BaseURL = "https://updated.acme.example/v1"
	provider.AuthType = AuthNone
	provider.TLSInsecure = false
	provider.Enabled = false
	provider.Revision = 2
	provider.UpdatedAt = createdAt.Add(2 * time.Minute)
	runCatalogTx(t, store, func(tx *sql.Tx) error {
		return UpdateProvider(ctx, tx, provider)
	})

	snapshot = loadCatalogSnapshot(t, store)
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers after update = %d, want 1", len(snapshot.Providers))
	}
	got = snapshot.Providers[0]
	if got.Name != provider.Name || got.BaseURL != provider.BaseURL || got.Kind != provider.Kind ||
		got.AuthType != provider.AuthType || got.TLSInsecure != provider.TLSInsecure ||
		got.Enabled != provider.Enabled || got.Revision != provider.Revision {
		t.Fatalf("provider after update = %+v, want %+v", got, provider)
	}
	requireTimeEqual(t, "created_at after update", got.CreatedAt, createdAt)
	requireTimeEqual(t, "updated_at after update", got.UpdatedAt, provider.UpdatedAt)

	deletedAt := provider.UpdatedAt.Add(3 * time.Minute)
	runCatalogTx(t, store, func(tx *sql.Tx) error {
		return SoftDeleteProvider(ctx, tx, provider.ID, deletedAt)
	})

	snapshot = loadCatalogSnapshot(t, store)
	if len(snapshot.Providers) != 0 {
		t.Fatalf("providers after soft delete = %d, want 0", len(snapshot.Providers))
	}

	var storedDeletedAt sql.NullString
	var storedEnabled, storedRevision int
	if err := store.DB.QueryRowContext(ctx,
		`SELECT deleted_at,enabled,revision FROM providers WHERE id=?`, provider.ID).
		Scan(&storedDeletedAt, &storedEnabled, &storedRevision); err != nil {
		t.Fatalf("read soft-deleted provider error = %v", err)
	}
	if !storedDeletedAt.Valid || storedDeletedAt.String == "" {
		t.Fatalf("deleted_at = %+v, want a non-empty timestamp", storedDeletedAt)
	}
	requireTimeEqual(t, "deleted_at", parseTime(storedDeletedAt.String), deletedAt)
	if storedEnabled != 0 {
		t.Fatalf("stored enabled = %d after soft delete, want 0", storedEnabled)
	}
	if storedRevision != provider.Revision+1 {
		t.Fatalf("stored revision = %d after soft delete, want %d", storedRevision, provider.Revision+1)
	}
}

func TestMigrateAddsTLSInsecureToExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE providers (
		id TEXT PRIMARY KEY,
		slug TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		base_url TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT 'openai_compatible',
		auth_type TEXT NOT NULL DEFAULT 'bearer',
		enabled INTEGER NOT NULL DEFAULT 1,
		revision INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		deleted_at TEXT
	)`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO providers
		(id,slug,name,base_url,kind,auth_type,enabled,revision,created_at,updated_at)
		VALUES('prv_old','old','Old','https://old.example/v1','openai_compatible','bearer',1,1,?,?)`,
		catalogTestTime().Format(time.RFC3339Nano), catalogTestTime().Format(time.RFC3339Nano)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := store.LoadData(ctx, store.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Providers) != 1 || data.Providers[0].TLSInsecure {
		t.Fatalf("migrated provider = %+v, want one provider with tls_insecure=false", data.Providers)
	}
}

func TestModelPersistenceAndDiscovery(t *testing.T) {
	ctx := context.Background()
	store := openCatalogTestStore(t)
	createdAt := catalogTestTime()
	provider := Provider{
		ID:        "prv_models",
		Slug:      "acme",
		Name:      "Acme",
		BaseURL:   "https://api.acme.example/v1",
		Kind:      ProviderOpenAICompatible,
		AuthType:  AuthBearer,
		Enabled:   true,
		Revision:  1,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
	runCatalogTx(t, store, func(tx *sql.Tx) error {
		return InsertProvider(ctx, tx, provider)
	})

	adminModel := Model{
		ID:              "mdl_admin",
		ProviderID:      provider.ID,
		UpstreamID:      "gpt-admin",
		PublicID:        "manual/gpt-admin",
		Enabled:         true,
		Stale:           false,
		Tools:           true,
		Vision:          true,
		MaxOutputTokens: 8192,
		Source:          "manual",
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
	runCatalogTx(t, store, func(tx *sql.Tx) error {
		return InsertModel(ctx, tx, adminModel)
	})

	snapshot := loadCatalogSnapshot(t, store)
	got := findModelByUpstream(t, snapshot, adminModel.UpstreamID)
	if got.ID != adminModel.ID || got.ProviderID != adminModel.ProviderID ||
		got.PublicID != adminModel.PublicID || got.Enabled != adminModel.Enabled ||
		got.Stale != adminModel.Stale || got.Tools != adminModel.Tools || got.Vision != adminModel.Vision ||
		got.MaxOutputTokens != adminModel.MaxOutputTokens || got.Source != adminModel.Source {
		t.Fatalf("model after insert = %+v, want %+v", got, adminModel)
	}
	requireTimeEqual(t, "model created_at", got.CreatedAt, adminModel.CreatedAt)
	requireTimeEqual(t, "model updated_at", got.UpdatedAt, adminModel.UpdatedAt)

	stateAt := createdAt.Add(5 * time.Minute)
	enabled := false
	tools := false
	vision := true
	maxOutput := 2048
	runCatalogTx(t, store, func(tx *sql.Tx) error {
		return SetModelState(ctx, tx, adminModel.ID, &enabled, &tools, &vision, &maxOutput, stateAt)
	})

	snapshot = loadCatalogSnapshot(t, store)
	got = findModelByUpstream(t, snapshot, adminModel.UpstreamID)
	if got.Enabled || got.Tools || !got.Vision || got.MaxOutputTokens != maxOutput {
		t.Fatalf("model state after SetModelState = %+v", got)
	}
	if got.Source != adminModel.Source {
		t.Fatalf("model source after SetModelState = %q, want %q", got.Source, adminModel.Source)
	}
	requireTimeEqual(t, "model updated_at after SetModelState", got.UpdatedAt, stateAt)

	discoveryAt := stateAt.Add(5 * time.Minute)
	var first DiscoveryResult
	runCatalogTx(t, store, func(tx *sql.Tx) error {
		var err error
		first, err = UpsertDiscoveredModels(ctx, tx, provider.ID, provider.Slug,
			[]string{adminModel.UpstreamID, "gpt-new"}, discoveryAt)
		return err
	})
	if first != (DiscoveryResult{Added: 1, Updated: 1}) {
		t.Fatalf("first discovery result = %+v, want added=1 updated=1", first)
	}

	snapshot = loadCatalogSnapshot(t, store)
	got = findModelByUpstream(t, snapshot, adminModel.UpstreamID)
	if got.PublicID != provider.Slug+"/"+adminModel.UpstreamID || got.Stale {
		t.Fatalf("admin model after discovery = %+v", got)
	}
	if got.Enabled || got.Tools || !got.Vision || got.MaxOutputTokens != maxOutput {
		t.Fatalf("discovery overwrote administrator settings: %+v", got)
	}
	if got.Source != "manual" {
		t.Fatalf("admin model source after discovery = %q, want manual", got.Source)
	}

	discovered := findModelByUpstream(t, snapshot, "gpt-new")
	if discovered.ID == "" || discovered.ProviderID != provider.ID ||
		discovered.PublicID != provider.Slug+"/gpt-new" || discovered.Enabled ||
		discovered.Stale || discovered.Tools || discovered.Vision ||
		discovered.MaxOutputTokens != DefaultModelOutputTokens || discovered.Source != "discovered" {
		t.Fatalf("newly discovered model = %+v", discovered)
	}
	requireTimeEqual(t, "discovered created_at", discovered.CreatedAt, discoveryAt)
	requireTimeEqual(t, "discovered updated_at", discovered.UpdatedAt, discoveryAt)

	var second DiscoveryResult
	runCatalogTx(t, store, func(tx *sql.Tx) error {
		var err error
		second, err = UpsertDiscoveredModels(ctx, tx, provider.ID, provider.Slug,
			[]string{"gpt-new"}, discoveryAt.Add(time.Minute))
		return err
	})
	if second != (DiscoveryResult{Updated: 1, Stale: 1}) {
		t.Fatalf("second discovery result = %+v, want updated=1 stale=1", second)
	}

	snapshot = loadCatalogSnapshot(t, store)
	got = findModelByUpstream(t, snapshot, adminModel.UpstreamID)
	if !got.Stale {
		t.Fatalf("missing admin model was not marked stale: %+v", got)
	}
	if got.Enabled || got.Tools || !got.Vision || got.MaxOutputTokens != maxOutput {
		t.Fatalf("stale update overwrote administrator settings: %+v", got)
	}
	discovered = findModelByUpstream(t, snapshot, "gpt-new")
	if discovered.Stale {
		t.Fatalf("present discovered model was marked stale: %+v", discovered)
	}
}

func TestClientKeyPersistence(t *testing.T) {
	ctx := context.Background()
	store := openCatalogTestStore(t)
	createdAt := catalogTestTime()
	lastUsedAt := createdAt.Add(15 * time.Minute)
	expiresAt := createdAt.Add(24 * time.Hour)
	key := ClientKey{
		ID:             "key_acme",
		Name:           "Acme integration",
		Prefix:         "am_live_acme",
		Hash:           []byte{0xde, 0xad, 0xbe, 0xef},
		Scopes:         []string{"chat", "admin"},
		Enabled:        true,
		RPM:            60,
		DailyRequests:  500,
		DailyTokens:    1_000_000,
		ModelAllowlist: []string{"acme/gpt-new", "acme/gpt-admin"},
		TenantID:       "tenant-acme",
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
		LastUsedAt:     &lastUsedAt,
		ExpiresAt:      &expiresAt,
	}

	runCatalogTx(t, store, func(tx *sql.Tx) error {
		return InsertClientKey(ctx, tx, key)
	})

	snapshot := loadCatalogSnapshot(t, store)
	if len(snapshot.Keys) != 1 {
		t.Fatalf("keys after insert = %d, want 1", len(snapshot.Keys))
	}
	got := snapshot.Keys[0]
	if got.ID != key.ID || got.Name != key.Name || got.Prefix != key.Prefix ||
		!bytes.Equal(got.Hash, key.Hash) || !slices.Equal(got.Scopes, key.Scopes) ||
		got.Enabled != key.Enabled || got.RPM != key.RPM ||
		got.DailyRequests != key.DailyRequests || got.DailyTokens != key.DailyTokens ||
		!slices.Equal(got.ModelAllowlist, key.ModelAllowlist) || got.TenantID != key.TenantID {
		t.Fatalf("client key after insert = %+v, want %+v", got, key)
	}
	requireTimeEqual(t, "client key created_at", got.CreatedAt, key.CreatedAt)
	requireTimeEqual(t, "client key updated_at", got.UpdatedAt, key.UpdatedAt)
	requireOptionalTimeEqual(t, "last_used_at", got.LastUsedAt, key.LastUsedAt)
	requireOptionalTimeEqual(t, "expires_at", got.ExpiresAt, key.ExpiresAt)
}

func TestWithTxRollsBackCatalogWrites(t *testing.T) {
	ctx := context.Background()
	store := openCatalogTestStore(t)
	createdAt := catalogTestTime()
	rollbackErr := errors.New("simulated bad configuration")
	provider := Provider{
		ID:        "prv_rollback",
		Slug:      "rollback",
		Name:      "Rollback",
		BaseURL:   "https://rollback.example/v1",
		Kind:      ProviderOpenAICompatible,
		AuthType:  AuthBearer,
		Enabled:   true,
		Revision:  1,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
	model := Model{
		ID:              "mdl_rollback",
		ProviderID:      provider.ID,
		UpstreamID:      "gpt-rollback",
		PublicID:        "rollback/gpt-rollback",
		Enabled:         true,
		MaxOutputTokens: 4096,
		Source:          "manual",
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
	key := ClientKey{
		ID:        "key_rollback",
		Name:      "Rollback",
		Prefix:    "am_live_rollback",
		Hash:      []byte{0x01, 0x02, 0x03},
		Scopes:    []string{"chat"},
		Enabled:   true,
		TenantID:  "tenant-rollback",
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}

	err := store.WithTx(ctx, func(tx *sql.Tx) error {
		if err := InsertProvider(ctx, tx, provider); err != nil {
			return err
		}
		if err := InsertModel(ctx, tx, model); err != nil {
			return err
		}
		if err := InsertClientKey(ctx, tx, key); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("WithTx() error = %v, want %v", err, rollbackErr)
	}

	snapshot := loadCatalogSnapshot(t, store)
	if len(snapshot.Providers) != 0 || len(snapshot.Models) != 0 || len(snapshot.Keys) != 0 {
		t.Fatalf("snapshot after rollback contains providers=%d models=%d keys=%d",
			len(snapshot.Providers), len(snapshot.Models), len(snapshot.Keys))
	}

	var providers, models, keys int
	if err := store.DB.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM providers),
		        (SELECT COUNT(*) FROM models),
		        (SELECT COUNT(*) FROM client_keys)`).Scan(&providers, &models, &keys); err != nil {
		t.Fatalf("count rolled-back rows error = %v", err)
	}
	if providers != 0 || models != 0 || keys != 0 {
		t.Fatalf("rows after rollback: providers=%d models=%d keys=%d", providers, models, keys)
	}
}

func TestCatalogConflictAndNotFound(t *testing.T) {
	ctx := context.Background()
	store := openCatalogTestStore(t)
	createdAt := catalogTestTime()
	provider := Provider{
		ID:        "prv_original",
		Slug:      "original",
		Name:      "Original",
		BaseURL:   "https://original.example/v1",
		Kind:      ProviderOpenAICompatible,
		AuthType:  AuthBearer,
		Enabled:   true,
		Revision:  1,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
	runCatalogTx(t, store, func(tx *sql.Tx) error {
		return InsertProvider(ctx, tx, provider)
	})

	duplicate := provider
	duplicate.ID = "prv_duplicate"
	err := store.WithTx(ctx, func(tx *sql.Tx) error {
		return InsertProvider(ctx, tx, duplicate)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate InsertProvider() error = %v, want %v", err, ErrConflict)
	}

	missing := provider
	missing.ID = "prv_missing"
	err = store.WithTx(ctx, func(tx *sql.Tx) error {
		return UpdateProvider(ctx, tx, missing)
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateProvider() missing error = %v, want %v", err, ErrNotFound)
	}

	disabled := false
	err = store.WithTx(ctx, func(tx *sql.Tx) error {
		return SetModelState(ctx, tx, "mdl_missing", &disabled, nil, nil, nil, createdAt)
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetModelState() missing error = %v, want %v", err, ErrNotFound)
	}

	snapshot := loadCatalogSnapshot(t, store)
	if len(snapshot.Providers) != 1 || snapshot.Providers[0].ID != provider.ID {
		t.Fatalf("providers after conflict paths = %+v, want only %q", snapshot.Providers, provider.ID)
	}
}

func openCatalogTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return store
}

func loadCatalogSnapshot(t *testing.T, store *Store) SnapshotData {
	t.Helper()
	snapshot, err := store.LoadData(context.Background(), store.DB)
	if err != nil {
		t.Fatalf("LoadData() error = %v", err)
	}
	return snapshot
}

func runCatalogTx(t *testing.T, store *Store, fn func(*sql.Tx) error) {
	t.Helper()
	if err := store.WithTx(context.Background(), fn); err != nil {
		t.Fatalf("WithTx() error = %v", err)
	}
}

func catalogTestTime() time.Time {
	return time.Date(2026, time.November, 16, 9, 30, 0, 123456789, time.UTC)
}

func requireTimeEqual(t *testing.T, label string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s = %s, want %s", label, got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

func requireOptionalTimeEqual(t *testing.T, label string, got, want *time.Time) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Errorf("%s = %v, want %v", label, got, want)
		}
		return
	}
	requireTimeEqual(t, label, *got, *want)
}

func findModelByUpstream(t *testing.T, snapshot SnapshotData, upstreamID string) Model {
	t.Helper()
	for _, model := range snapshot.Models {
		if model.UpstreamID == upstreamID {
			return model
		}
	}
	t.Fatalf("model with upstream_id %q not found in %+v", upstreamID, snapshot.Models)
	return Model{}
}
