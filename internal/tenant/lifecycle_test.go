package tenant

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

func TestMySQLLifecycleCreatesTenantAndOrderedRoutesInOneTransaction(t *testing.T) {
	tx := &fakeLifecycleTransaction{}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	repository := newMySQLLifecycle(fakeLifecycleDatabase{tx: tx}, func() time.Time { return now }, nil)
	err := repository.CreateTenant(context.Background(), Tenant{ID: "tenant-a", ModelRoutes: map[string][]string{"model-a": {"ark", "ollama"}}})
	if err != nil || !tx.committed || len(tx.execQueries) != 3 {
		t.Fatalf("err=%v committed=%t queries=%q", err, tx.committed, tx.execQueries)
	}
	if !strings.Contains(tx.execQueries[0], "INSERT INTO tenants") || !strings.Contains(tx.execQueries[1], "ordinal") {
		t.Fatalf("queries=%q", tx.execQueries)
	}
}

func TestMySQLLifecycleKeyPrefixConflictRetriesWithoutPersistingRawKey(t *testing.T) {
	tx := &fakeLifecycleTransaction{
		rows:     []lifecycleRow{boolLifecycleRow(true)},
		execErrs: []error{&mysql.MySQLError{Number: 1062}, nil},
	}
	values := [][2]string{{"key-id-1", "aaaaaaaa-first-raw-key"}, {"key-id-2", "bbbbbbbb-second-raw-key"}}
	next := 0
	repository := newMySQLLifecycle(fakeLifecycleDatabase{tx: tx}, time.Now, func() (string, string, error) {
		value := values[next]
		next++
		return value[0], value[1], nil
	})
	key, raw, err := repository.CreateAPIKey(context.Background(), "tenant-a")
	if err != nil || raw != values[1][1] || key.ID != values[1][0] || !tx.committed {
		t.Fatalf("key=%+v raw=%q err=%v committed=%t", key, raw, err, tx.committed)
	}
	if len(tx.execArgs) != 2 || strings.Contains(strings.Join(arguments(tx.execArgs), " "), values[0][1]) || strings.Contains(strings.Join(arguments(tx.execArgs), " "), values[1][1]) {
		t.Fatalf("raw key leaked into SQL args: %q", arguments(tx.execArgs))
	}
}

func TestMySQLLifecycleRevokeIsIdempotentButUnknownKeyFails(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		row     lifecycleRow
		wantErr error
	}{
		{name: "existing key", row: boolLifecycleRow(false)},
		{name: "already revoked", row: boolLifecycleRow(false)},
		{name: "unknown key", row: errLifecycleRow{sql.ErrNoRows}, wantErr: ErrKeyNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			tx := &fakeLifecycleTransaction{rows: []lifecycleRow{testCase.row}}
			repository := newMySQLLifecycle(fakeLifecycleDatabase{tx: tx}, time.Now, nil)
			err := repository.RevokeAPIKey(context.Background(), "key-id")
			if !errors.Is(err, testCase.wantErr) || (testCase.wantErr == nil && (!tx.committed || len(tx.execQueries) != 1)) {
				t.Fatalf("err=%v committed=%t queries=%q", err, tx.committed, tx.execQueries)
			}
		})
	}
}

type fakeLifecycleDatabase struct{ tx *fakeLifecycleTransaction }

func (d fakeLifecycleDatabase) BeginTx(context.Context, *sql.TxOptions) (lifecycleTransaction, error) {
	return d.tx, nil
}

type fakeLifecycleTransaction struct {
	rows        []lifecycleRow
	execErrs    []error
	execQueries []string
	execArgs    [][]any
	committed   bool
	rolledBack  bool
}

func (t *fakeLifecycleTransaction) ExecContext(_ context.Context, query string, args ...any) (lifecycleResult, error) {
	t.execQueries = append(t.execQueries, query)
	t.execArgs = append(t.execArgs, args)
	if len(t.execErrs) > 0 {
		err := t.execErrs[0]
		t.execErrs = t.execErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return fakeLifecycleResult(1), nil
}
func (t *fakeLifecycleTransaction) QueryRowContext(_ context.Context, _ string, _ ...any) lifecycleRow {
	if len(t.rows) == 0 {
		return errLifecycleRow{errors.New("unexpected query")}
	}
	value := t.rows[0]
	t.rows = t.rows[1:]
	return value
}
func (t *fakeLifecycleTransaction) Commit() error { t.committed = true; return nil }
func (t *fakeLifecycleTransaction) Rollback() error {
	if !t.committed {
		t.rolledBack = true
	}
	return nil
}

type fakeLifecycleResult int64

func (r fakeLifecycleResult) RowsAffected() (int64, error) { return int64(r), nil }

type boolLifecycleRow bool

func (r boolLifecycleRow) Scan(destinations ...any) error {
	*destinations[0].(*bool) = bool(r)
	return nil
}

type errLifecycleRow struct{ err error }

func (r errLifecycleRow) Scan(...any) error { return r.err }

func arguments(values [][]any) []string {
	result := make([]string, 0, len(values))
	for _, group := range values {
		result = append(result, strings.TrimSpace(strings.Join(stringify(group), " ")))
	}
	return result
}
func stringify(values []any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, "[value]")
		if text, ok := value.(string); ok {
			result[len(result)-1] = text
		}
	}
	return result
}
