package reservation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRealValidationConfigRejectsBeforeAnyExternalOpen(t *testing.T) {
	lookups := 0
	_, report, err := LoadRealValidationConfig(func(string) string { lookups++; return "" })
	if Code(err) != CodeQuotaConfigurationMissing || report.Status != "verification_unavailable" || report.NetworkAttempts != 0 || report.ProviderAttempts != 0 {
		t.Fatalf("err=%v report=%+v", err, report)
	}
	if lookups != 1 {
		t.Fatalf("missing opt-in must stop before reading connection values, lookups=%d", lookups)
	}
}

func TestValidateRealStorageStopsBeforeReadingMigrationOrOpeningStores(t *testing.T) {
	report, err := ValidateRealStorage(context.Background(), func(string) string { return "" }, "missing-migration.sql")
	if Code(err) != CodeQuotaConfigurationMissing || report.Status != "verification_unavailable" || report.NetworkAttempts != 0 || report.ProviderAttempts != 0 {
		t.Fatalf("err=%v report=%+v", err, report)
	}
}

func TestLoadRealValidationConfigDoesNotSerializeSecretsAndValidatesNamespace(t *testing.T) {
	values := map[string]string{
		realValidationOptInEnvironment:     "1",
		quotaMySQLDSNEnvironment:           "user:super-secret@tcp(mysql.internal:3306)/quota",
		quotaRedisURLEnvironment:           "redis://:other-secret@redis.internal:6379/0",
		realValidationNamespaceEnvironment: "interview-008",
	}
	config, report, err := LoadRealValidationConfig(func(name string) string { return values[name] })
	if err != nil || config.Namespace != "interview-008" || report.Status != "verification_pending" {
		t.Fatalf("config=%+v report=%+v err=%v", config, report, err)
	}
	encoded, err := json.Marshal(struct {
		Config RealValidationConfig `json:"config"`
		Report ValidationReport     `json:"report"`
	}{config, report})
	if err != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "internal") {
		t.Fatalf("safe json=%s err=%v", encoded, err)
	}
	values[realValidationNamespaceEnvironment] = "UPPER"
	_, rejected, err := LoadRealValidationConfig(func(name string) string { return values[name] })
	if Code(err) != CodeValidationNamespaceInvalid || rejected.Code != CodeValidationNamespaceInvalid || rejected.NetworkAttempts != 0 {
		t.Fatalf("err=%v report=%+v", err, rejected)
	}
}

func TestParseAndEnsureQuotaMigrationOnlyCreatesMissingTables(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "migrations", "001_quota_reservations.sql"))
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := ParseQuotaMigration(string(content))
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeMigrationStore{shapes: map[string]MigrationTableShape{
		"quota_reservations": expectedMigrationShape("quota_reservations"),
		"provider_attempts":  {Exists: false},
	}}
	report, err := EnsureQuotaMigration(context.Background(), store, definitions)
	if err != nil || len(store.creates) != 1 || store.creates[0] != "provider_attempts" || len(report.Tables) != 2 || report.Tables[0].Created || !report.Tables[1].Created {
		t.Fatalf("report=%+v creates=%v err=%v", report, store.creates, err)
	}
	if strings.Contains(strings.ToUpper(strings.Join(store.statements, "\n")), "DROP") || strings.Contains(strings.ToUpper(strings.Join(store.statements, "\n")), "ALTER") {
		t.Fatalf("migration executor must never use destructive repair: %q", store.statements)
	}
}

func TestEnsureQuotaMigrationStopsOnExistingSchemaMismatch(t *testing.T) {
	store := &fakeMigrationStore{shapes: map[string]MigrationTableShape{
		"quota_reservations": {Exists: true, Columns: map[string]bool{"reservation_id": true}, Indexes: map[string]bool{}, Constraints: map[string]bool{}},
		"provider_attempts":  expectedMigrationShape("provider_attempts"),
	}}
	_, err := EnsureQuotaMigration(context.Background(), store, []MigrationDefinition{{Table: "quota_reservations", Statement: "CREATE TABLE quota_reservations (...)"}, {Table: "provider_attempts", Statement: "CREATE TABLE provider_attempts (...)"}})
	if Code(err) != CodeMigrationSchemaMismatch || len(store.creates) != 0 {
		t.Fatalf("code=%q creates=%v", Code(err), store.creates)
	}
}

func TestRunValidationScenariosChecksFactsAndCleansExactScope(t *testing.T) {
	scope, err := NewValidationScope("interview-008", "run-a")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeValidationScenarioStore{results: expectedValidationScenarios}
	report, err := RunValidationScenarios(context.Background(), store, scope)
	if err != nil || report.Status != "verification_completed" || len(report.Scenarios) != 4 || !report.Cleanup.Attempted || !report.Cleanup.Completed || store.cleanupScope != scope {
		t.Fatalf("report=%+v cleanup=%+v err=%v", report, store.cleanupScope, err)
	}
	if strings.Contains(scope.Tenant("started"), "*") || !strings.Contains(scope.Tenant("started"), "interview-008") {
		t.Fatalf("tenant scope=%q", scope.Tenant("started"))
	}
}

func TestRunValidationScenariosReportsFailureAndStillAttemptsCleanup(t *testing.T) {
	scope, err := NewValidationScope("interview-008", "run-b")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeValidationScenarioStore{results: expectedValidationScenarios, failScenario: "started_reconcile", cleanupErr: errors.New("cleanup unavailable")}
	report, err := RunValidationScenarios(context.Background(), store, scope)
	if Code(err) != CodeValidationScenarioFailed || report.Code != CodeValidationScenarioFailed || !report.Cleanup.Attempted || report.Cleanup.Completed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

type fakeMigrationStore struct {
	shapes     map[string]MigrationTableShape
	creates    []string
	statements []string
}

func (f *fakeMigrationStore) TableShape(_ context.Context, table string) (MigrationTableShape, error) {
	return f.shapes[table], nil
}

func (f *fakeMigrationStore) ExecuteCreate(_ context.Context, statement string) error {
	fields := strings.Fields(statement)
	if len(fields) < 3 || !strings.EqualFold(fields[0], "CREATE") || !strings.EqualFold(fields[1], "TABLE") {
		return errors.New("unexpected create statement")
	}
	table := strings.Trim(fields[2], "` ")
	f.creates = append(f.creates, table)
	f.statements = append(f.statements, statement)
	f.shapes[table] = expectedMigrationShape(table)
	return nil
}

type fakeValidationScenarioStore struct {
	results      map[string]ValidationScenarioReport
	failScenario string
	cleanupErr   error
	cleanupScope ValidationScope
}

func (f *fakeValidationScenarioStore) RunScenario(_ context.Context, _ ValidationScope, name string) (ValidationScenarioReport, error) {
	if name == f.failScenario {
		return ValidationScenarioReport{}, errors.New("scenario unavailable")
	}
	return f.results[name], nil
}

func (f *fakeValidationScenarioStore) Cleanup(_ context.Context, scope ValidationScope) error {
	f.cleanupScope = scope
	return f.cleanupErr
}
