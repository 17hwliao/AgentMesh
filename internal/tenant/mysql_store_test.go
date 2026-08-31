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
			value, ok := NewMySQLStore(db).Authenticate(context.Background(), raw[:8], digest)
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
	route, ok := store.Route(context.Background(), "tenant-a", "model-a")
	if !ok || !reflect.DeepEqual(route, []string{"ark", "ollama"}) {
		t.Fatalf("route=%q ok=%t", route, ok)
	}
	if routes := store.Routes(context.Background(), "tenant-a"); !reflect.DeepEqual(routes, [][]string{{"ark", "ollama"}}) {
		t.Fatalf("routes=%q", routes)
	}
	if len(db.queryQueries) != 2 || !strings.Contains(db.queryQueries[0], "r.tenant_id = ?") {
		t.Fatalf("queries=%q", db.queryQueries)
	}
}

func TestMySQLStoreLoadsDeclaredRoutesAtStartup(t *testing.T) {
	db := &fakeReadDatabase{rows: []mysqlRows{&fakeRows{values: [][]string{{"tenant-a", "model-a", "ark"}, {"tenant-a", "model-a", "ollama"}, {"tenant-b", "model-b", "ark"}}}}, row: countRow{tenants: 2, routes: 3, keys: 2}}
	state, err := NewMySQLStore(db).LoadStartupState(context.Background())
	if err != nil || state.Pristine || !reflect.DeepEqual(state.Routes, [][]string{{"ark", "ollama"}, {"ark"}}) {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if len(db.queryQueries) != 1 || db.queryQueries[0] != allRoutesMySQL {
		t.Fatalf("queries=%q", db.queryQueries)
	}
}

func TestMySQLStoreStartupStateKeepsPristinePartialAndErrorsDistinct(t *testing.T) {
	tests := []struct {
		name           string
		row            mysqlRow
		rows           []mysqlRows
		queryErr       error
		pristine       bool
		wantError      bool
		wantRouteQuery bool
	}{
		{name: "all three tables empty is pristine", row: countRow{}, pristine: true},
		{name: "partial identity data is not pristine", row: countRow{tenants: 1}, rows: []mysqlRows{&fakeRows{}}, wantRouteQuery: true},
		{name: "route query error remains an error", row: countRow{tenants: 1}, queryErr: errors.New("database unavailable"), wantError: true, wantRouteQuery: true},
		{name: "count query error remains an error", row: countErrorRow{err: errors.New("database unavailable")}, wantError: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			db := &fakeReadDatabase{row: testCase.row, rows: testCase.rows, queryErrors: []error{testCase.queryErr}}
			state, err := NewMySQLStore(db).LoadStartupState(context.Background())
			if (err != nil) != testCase.wantError || state.Pristine != testCase.pristine {
				t.Fatalf("state=%+v err=%v", state, err)
			}
			if len(db.rowQueries) != 1 || db.rowQueries[0] != identityCountsMySQL {
				t.Fatalf("count queries=%q", db.rowQueries)
			}
			if testCase.wantRouteQuery && len(db.queryQueries) != 1 {
				t.Fatalf("route query calls=%q", db.queryQueries)
			}
			if !testCase.wantRouteQuery && len(db.queryQueries) != 0 {
				t.Fatalf("unexpected route query=%q", db.queryQueries)
			}
		})
	}
}

func TestMySQLStoreForwardsCallerContextToEveryRead(t *testing.T) {
	type marker struct{}
	ctx := context.WithValue(context.Background(), marker{}, "caller")
	digest := sha256.Sum256([]byte("12345678-tenant-key-material"))
	db := &fakeReadDatabase{
		rowQueue: []mysqlRow{
			authRow{hash: digest[:], keyEnabled: true, tenantID: "tenant-a", tenantEnabled: true},
			countRow{tenants: 1, routes: 1, keys: 1},
		},
		rows: []mysqlRows{
			&fakeRows{values: [][]string{{"ark"}}},
			&fakeRows{values: [][]string{{"model-a", "ark"}}},
			&fakeRows{values: [][]string{{"tenant-a", "model-a", "ark"}}},
		},
	}
	store := NewMySQLStore(db)
	store.Authenticate(ctx, "12345678", digest)
	store.Route(ctx, "tenant-a", "model-a")
	store.Routes(ctx, "tenant-a")
	_, _ = store.LoadStartupState(ctx)
	for _, captured := range append(db.rowContexts, db.queryContexts...) {
		if captured == nil || captured.Value(marker{}) != "caller" {
			t.Fatalf("context was not forwarded: %v", captured)
		}
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
	row           mysqlRow
	rowQueue      []mysqlRow
	rows          []mysqlRows
	queryErrors   []error
	rowQueries    []string
	queryQueries  []string
	rowContexts   []context.Context
	queryContexts []context.Context
}

func (d *fakeReadDatabase) QueryRowContext(ctx context.Context, query string, _ ...any) mysqlRow {
	d.rowQueries = append(d.rowQueries, query)
	d.rowContexts = append(d.rowContexts, ctx)
	if len(d.rowQueue) > 0 {
		row := d.rowQueue[0]
		d.rowQueue = d.rowQueue[1:]
		return row
	}
	return d.row
}
func (d *fakeReadDatabase) QueryContext(ctx context.Context, query string, _ ...any) (mysqlRows, error) {
	d.queryQueries = append(d.queryQueries, query)
	d.queryContexts = append(d.queryContexts, ctx)
	if len(d.queryErrors) > 0 {
		err := d.queryErrors[0]
		d.queryErrors = d.queryErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(d.rows) == 0 {
		return nil, errors.New("unexpected query")
	}
	result := d.rows[0]
	d.rows = d.rows[1:]
	return result, nil
}

type countErrorRow struct{ err error }

func (r countErrorRow) Scan(...any) error { return r.err }

type authRow struct {
	hash                      []byte
	keyEnabled, tenantEnabled bool
	tenantID                  string
	err                       error
}

type countRow struct{ tenants, routes, keys uint64 }

func (r countRow) Scan(destinations ...any) error {
	*destinations[0].(*uint64) = r.tenants
	*destinations[1].(*uint64) = r.routes
	*destinations[2].(*uint64) = r.keys
	return nil
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
