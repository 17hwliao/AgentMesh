package tenant

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMySQLStoreAuthenticatesAndRejectsUnknownOrDisabled(t *testing.T) {
	raw := "12345678-tenant-key-material"
	digest := sha256.Sum256([]byte(raw))
	for _, testCase := range []struct {
		name       string
		row        mysqlRow
		wantTenant string
	}{
		{name: "valid", row: authRow{hash: digest[:], keyEnabled: true, tenantID: "tenant-a", tenantEnabled: true}, wantTenant: "tenant-a"},
		{name: "unknown prefix uses dummy digest", row: authRow{err: sql.ErrNoRows}},
		{name: "disabled key", row: authRow{hash: digest[:], keyEnabled: false, tenantID: "tenant-a", tenantEnabled: true}},
		{name: "disabled tenant", row: authRow{hash: digest[:], keyEnabled: true, tenantID: "tenant-a", tenantEnabled: false}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := &fakeReadDatabase{row: testCase.row}
			value, ok := NewMySQLStore(db).Authenticate(raw[:8], digest)
			if ok != (testCase.wantTenant != "") || value.ID != testCase.wantTenant {
				t.Fatalf("tenant=%+v ok=%t", value, ok)
			}
			if len(db.rowQueries) != 1 || db.rowQueries[0] != authenticateMySQL {
				t.Fatalf("authentication query=%q", db.rowQueries)
			}
		})
	}
}

func TestMySQLStoreReadsOrderedRoutesWithoutCrossTenantVisibility(t *testing.T) {
	db := &fakeReadDatabase{rows: []mysqlRows{&fakeRows{values: [][]string{{"ark"}, {"ollama"}}}, &fakeRows{values: [][]string{{"model-a", "ark"}, {"model-a", "ollama"}}}}}
	store := NewMySQLStore(db)
	route, ok := store.Route("tenant-a", "model-a")
	if !ok || !reflect.DeepEqual(route, []string{"ark", "ollama"}) {
		t.Fatalf("route=%q ok=%t", route, ok)
	}
	if routes := store.Routes("tenant-a"); !reflect.DeepEqual(routes, [][]string{{"ark", "ollama"}}) {
		t.Fatalf("routes=%q", routes)
	}
	if len(db.queryQueries) != 2 || !strings.Contains(db.queryQueries[0], "r.tenant_id = ?") {
		t.Fatalf("queries=%q", db.queryQueries)
	}
}

func TestMySQLStoreAllRoutesLoadsPersistedRoutesAtStartup(t *testing.T) {
	db := &fakeReadDatabase{rows: []mysqlRows{&fakeRows{values: [][]string{{"tenant-a", "model-a", "ark"}, {"tenant-a", "model-a", "ollama"}, {"tenant-b", "model-b", "ark"}}}}}
	routes := NewMySQLStore(db).AllRoutes()
	if !reflect.DeepEqual(routes, [][]string{{"ark", "ollama"}, {"ark"}}) {
		t.Fatalf("routes=%q", routes)
	}
	if len(db.queryQueries) != 1 || db.queryQueries[0] != allRoutesMySQL {
		t.Fatalf("queries=%q", db.queryQueries)
	}
}

func TestTenantKeyMigrationDefinesOnlyDerivedCredentials(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "003_tenant_api_keys.sql")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{"CREATE TABLE IF NOT EXISTS tenants", "CREATE TABLE IF NOT EXISTS tenant_model_routes", "CREATE TABLE IF NOT EXISTS api_keys", "key_hash BINARY(32)", "uq_api_keys_prefix", "fk_api_keys_tenant"} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, prohibited := range []string{"raw_key", "admin_token", "drop table", "alter table", "insert into"} {
		if strings.Contains(strings.ToLower(text), prohibited) {
			t.Fatalf("migration must not contain %q", prohibited)
		}
	}
}

type fakeReadDatabase struct {
	row          mysqlRow
	rows         []mysqlRows
	rowQueries   []string
	queryQueries []string
}

func (d *fakeReadDatabase) QueryRowContext(_ context.Context, query string, _ ...any) mysqlRow {
	d.rowQueries = append(d.rowQueries, query)
	return d.row
}
func (d *fakeReadDatabase) QueryContext(_ context.Context, query string, _ ...any) (mysqlRows, error) {
	d.queryQueries = append(d.queryQueries, query)
	if len(d.rows) == 0 {
		return nil, errors.New("unexpected query")
	}
	result := d.rows[0]
	d.rows = d.rows[1:]
	return result, nil
}

type authRow struct {
	hash                      []byte
	keyEnabled, tenantEnabled bool
	tenantID                  string
	err                       error
}

func (r authRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	*destinations[0].(*[]byte) = append([]byte(nil), r.hash...)
	*destinations[1].(*bool) = r.keyEnabled
	*destinations[2].(*string) = r.tenantID
	*destinations[3].(*bool) = r.tenantEnabled
	return nil
}

type fakeRows struct {
	values [][]string
	next   int
}

func (r *fakeRows) Next() bool { return r.next < len(r.values) }
func (r *fakeRows) Scan(destinations ...any) error {
	if r.next >= len(r.values) {
		return errors.New("unexpected scan")
	}
	for index, value := range r.values[r.next] {
		*destinations[index].(*string) = value
	}
	r.next++
	return nil
}
func (*fakeRows) Err() error   { return nil }
func (*fakeRows) Close() error { return nil }
