package reservation

import (
	"context"
	"sort"
	"strings"
)

const (
	realValidationOptInEnvironment     = "AGENTMESH_REAL_STORAGE_VALIDATION"
	realValidationNamespaceEnvironment = "AGENTMESH_REAL_VALIDATION_NAMESPACE"
	CodeMigrationDefinitionInvalid     = "migration_definition_invalid"
	CodeMigrationSchemaMismatch        = "migration_schema_mismatch"
	CodeValidationNamespaceInvalid     = "validation_namespace_invalid"
	CodeValidationAssertionFailed      = "validation_assertion_failed"
	CodeValidationScenarioFailed       = "validation_scenario_failed"
	CodeValidationCleanupFailed        = "validation_cleanup_failed"
)

// RealValidationConfig contains connection values only while the process is
// running. Its JSON exclusion makes accidental report serialization harmless.
type RealValidationConfig struct {
	MySQLDSN  string `json:"-"`
	RedisURL  string `json:"-"`
	Namespace string `json:"-"`
}

// ValidationReport is deliberately a safe operational summary. It never
// carries endpoint, credential, request, reservation, or response text.
type ValidationReport struct {
	Status           string                     `json:"status"`
	Code             string                     `json:"code,omitempty"`
	NetworkAttempts  int                        `json:"network_attempts"`
	ProviderAttempts int                        `json:"provider_attempts"`
	Migration        MigrationReport            `json:"migration,omitempty"`
	Scenarios        []ValidationScenarioReport `json:"scenarios,omitempty"`
	Cleanup          ValidationCleanupReport    `json:"cleanup,omitempty"`
}

type ValidationCleanupReport struct {
	Attempted bool `json:"attempted"`
	Completed bool `json:"completed"`
}

func unavailableValidationReport(code string) ValidationReport {
	return ValidationReport{Status: "verification_unavailable", Code: code, NetworkAttempts: 0, ProviderAttempts: 0}
}

// LoadRealValidationConfig is intentionally pure: missing or invalid
// configuration cannot allocate a client or send a network request.
func LoadRealValidationConfig(lookup func(string) string) (RealValidationConfig, ValidationReport, error) {
	if lookup == nil || strings.TrimSpace(lookup(realValidationOptInEnvironment)) != "1" {
		return RealValidationConfig{}, unavailableValidationReport(CodeQuotaConfigurationMissing), domainError(CodeQuotaConfigurationMissing)
	}
	config := RealValidationConfig{
		MySQLDSN:  strings.TrimSpace(lookup(quotaMySQLDSNEnvironment)),
		RedisURL:  strings.TrimSpace(lookup(quotaRedisURLEnvironment)),
		Namespace: strings.TrimSpace(lookup(realValidationNamespaceEnvironment)),
	}
	if config.MySQLDSN == "" || config.RedisURL == "" || config.Namespace == "" {
		return RealValidationConfig{}, unavailableValidationReport(CodeQuotaConfigurationMissing), domainError(CodeQuotaConfigurationMissing)
	}
	if !validValidationNamespace(config.Namespace) {
		return RealValidationConfig{}, unavailableValidationReport(CodeValidationNamespaceInvalid), domainError(CodeValidationNamespaceInvalid)
	}
	return config, ValidationReport{Status: "verification_pending", ProviderAttempts: 0}, nil
}

func validValidationNamespace(value string) bool {
	if len(value) == 0 || len(value) > 32 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

// ValidationScope makes every generated record and key traceable to one
// namespace while avoiding a wildcard cleanup operation.
type ValidationScope struct {
	Namespace string
	RunID     string
}

func NewValidationScope(namespace, runID string) (ValidationScope, error) {
	if !validValidationNamespace(namespace) || !validValidationRunID(runID) {
		return ValidationScope{}, domainError(CodeValidationNamespaceInvalid)
	}
	return ValidationScope{Namespace: namespace, RunID: runID}, nil
}

func validValidationRunID(value string) bool {
	return len(value) <= 12 && validValidationNamespace(value)
}

func (s ValidationScope) Tenant(caseName string) string {
	return "validation-" + s.Namespace + "-" + s.RunID + "-" + caseName
}

func (s ValidationScope) ReservationID(caseName string) string {
	return "rv-" + s.RunID + "-" + caseName
}

func (s ValidationScope) RequestID(caseName string) string {
	return "rq-" + s.RunID + "-" + caseName
}

// MigrationDefinition is parsed from the existing migration rather than being
// duplicated in the validator, so the real run cannot silently use different DDL.
type MigrationDefinition struct {
	Table     string
	Statement string
}

type MigrationTableShape struct {
	Exists      bool
	Columns     map[string]bool
	Indexes     map[string]bool
	Constraints map[string]bool
}

type MigrationTableReport struct {
	Table    string `json:"table"`
	Created  bool   `json:"created"`
	Verified bool   `json:"verified"`
}

type MigrationReport struct {
	Tables []MigrationTableReport `json:"tables,omitempty"`
}

type migrationStore interface {
	TableShape(context.Context, string) (MigrationTableShape, error)
	ExecuteCreate(context.Context, string) error
}

func ParseQuotaMigration(content string) ([]MigrationDefinition, error) {
	var definitions []MigrationDefinition
	for _, statement := range strings.Split(content, ";") {
		trimmed := strings.TrimSpace(statement)
		upper := strings.ToUpper(trimmed)
		if !strings.Contains(upper, "CREATE TABLE") {
			continue
		}
		fields := strings.Fields(trimmed)
		for index := 0; index+2 < len(fields); index++ {
			if strings.EqualFold(fields[index], "CREATE") && strings.EqualFold(fields[index+1], "TABLE") {
				table := strings.Trim(fields[index+2], "` ")
				definitions = append(definitions, MigrationDefinition{Table: table, Statement: trimmed + ";"})
				break
			}
		}
	}
	if len(definitions) != 2 || definitions[0].Table != "quota_reservations" || definitions[1].Table != "provider_attempts" {
		return nil, domainError(CodeMigrationDefinitionInvalid)
	}
	return definitions, nil
}

func expectedMigrationShape(table string) MigrationTableShape {
	shape := MigrationTableShape{Exists: true, Columns: map[string]bool{}, Indexes: map[string]bool{}, Constraints: map[string]bool{}}
	switch table {
	case "quota_reservations":
		for _, value := range []string{"reservation_id", "tenant_id", "request_id", "model", "state", "version", "reserved_units", "settled_units", "released_units", "usage_observed", "settlement_kind", "heartbeat_at", "created_at", "updated_at"} {
			shape.Columns[value] = true
		}
		shape.Indexes["PRIMARY"] = true
		shape.Indexes["uq_quota_reservations_tenant_request"] = true
		shape.Indexes["ix_quota_reservations_reconcile"] = true
		shape.Constraints["chk_quota_reservations_state"] = true
	case "provider_attempts":
		for _, value := range []string{"attempt_id", "reservation_id", "ordinal", "provider_name", "model", "result_code", "started_at", "finished_at", "heartbeat_at", "forwarded_runes", "provider_input_units", "provider_output_units", "usage_observed"} {
			shape.Columns[value] = true
		}
		shape.Indexes["PRIMARY"] = true
		shape.Indexes["uq_provider_attempts_reservation_ordinal"] = true
		shape.Indexes["ix_provider_attempts_reservation_started"] = true
		shape.Constraints["fk_provider_attempts_reservation"] = true
	}
	return shape
}

func matchesMigrationShape(actual, expected MigrationTableShape) bool {
	if !actual.Exists || !expected.Exists {
		return actual.Exists == expected.Exists
	}
	for _, collection := range [][2]map[string]bool{{actual.Columns, expected.Columns}, {actual.Indexes, expected.Indexes}, {actual.Constraints, expected.Constraints}} {
		for name := range collection[1] {
			if !collection[0][name] {
				return false
			}
		}
	}
	return true
}

// EnsureQuotaMigration only creates a missing whole table. Existing shapes
// are verified before any DML scenario can run; it never alters or drops data.
func EnsureQuotaMigration(ctx context.Context, store migrationStore, definitions []MigrationDefinition) (MigrationReport, error) {
	if store == nil || len(definitions) != 2 {
		return MigrationReport{}, domainError(CodeMigrationDefinitionInvalid)
	}
	report := MigrationReport{Tables: make([]MigrationTableReport, 0, len(definitions))}
	for _, definition := range definitions {
		expected := expectedMigrationShape(definition.Table)
		if len(expected.Columns) == 0 || definition.Statement == "" {
			return report, domainError(CodeMigrationDefinitionInvalid)
		}
		shape, err := store.TableShape(ctx, definition.Table)
		if err != nil {
			return report, err
		}
		entry := MigrationTableReport{Table: definition.Table}
		if !shape.Exists {
			if err := store.ExecuteCreate(ctx, definition.Statement); err != nil {
				return report, err
			}
			entry.Created = true
			shape, err = store.TableShape(ctx, definition.Table)
			if err != nil {
				return report, err
			}
		}
		if !matchesMigrationShape(shape, expected) {
			return report, domainError(CodeMigrationSchemaMismatch)
		}
		entry.Verified = true
		report.Tables = append(report.Tables, entry)
	}
	return report, nil
}

type ValidationScenarioReport struct {
	Name             string `json:"name"`
	FinalState       State  `json:"final_state"`
	AvailableUnits   uint64 `json:"available_units"`
	ReplayUnits      uint64 `json:"replay_units"`
	ReleasedUnits    uint64 `json:"released_units"`
	UsageObserved    bool   `json:"usage_observed"`
	SettlementKind   string `json:"settlement_kind"`
	ProviderAttempts int    `json:"provider_attempts"`
}

type validationScenarioStore interface {
	RunScenario(context.Context, ValidationScope, string) (ValidationScenarioReport, error)
	Cleanup(context.Context, ValidationScope) error
}

var expectedValidationScenarios = map[string]ValidationScenarioReport{
	"settle_replay":       {Name: "settle_replay", FinalState: Settled, AvailableUnits: 76, ReplayUnits: 76, ReleasedUnits: 40, UsageObserved: true, SettlementKind: "provider_usage", ProviderAttempts: 0},
	"cancel_replay":       {Name: "cancel_replay", FinalState: Cancelled, AvailableUnits: 100, ReplayUnits: 100, ReleasedUnits: 64, SettlementKind: "cancelled", ProviderAttempts: 0},
	"started_reconcile":   {Name: "started_reconcile", FinalState: Settled, AvailableUnits: 36, ReplayUnits: 36, ReleasedUnits: 0, SettlementKind: "estimated", ProviderAttempts: 0},
	"unstarted_reconcile": {Name: "unstarted_reconcile", FinalState: Cancelled, AvailableUnits: 100, ReplayUnits: 100, ReleasedUnits: 64, SettlementKind: "cancelled", ProviderAttempts: 0},
}

// RunValidationScenarios validates the known real-store observations without
// knowing credentials or a database driver. Cleanup remains exact and is
// attempted even after a scenario failure.
func RunValidationScenarios(ctx context.Context, store validationScenarioStore, scope ValidationScope) (report ValidationReport, err error) {
	if store == nil || !validValidationNamespace(scope.Namespace) || !validValidationRunID(scope.RunID) {
		return ValidationReport{Status: "verification_failed", Code: CodeValidationNamespaceInvalid, ProviderAttempts: 0}, domainError(CodeValidationNamespaceInvalid)
	}
	report = ValidationReport{Status: "verification_failed", ProviderAttempts: 0, Scenarios: make([]ValidationScenarioReport, 0, len(expectedValidationScenarios))}
	defer func() {
		report.Cleanup.Attempted = true
		if cleanupErr := store.Cleanup(ctx, scope); cleanupErr != nil {
			report.Cleanup.Completed = false
			if err == nil {
				report.Code = CodeValidationCleanupFailed
				err = domainError(CodeValidationCleanupFailed)
			}
			return
		}
		report.Cleanup.Completed = true
	}()

	names := make([]string, 0, len(expectedValidationScenarios))
	for name := range expectedValidationScenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		observation, runErr := store.RunScenario(ctx, scope, name)
		if runErr != nil {
			report.Code = CodeValidationScenarioFailed
			return report, domainError(CodeValidationScenarioFailed)
		}
		expected := expectedValidationScenarios[name]
		if observation != expected {
			report.Code = CodeValidationAssertionFailed
			return report, domainError(CodeValidationAssertionFailed)
		}
		report.Scenarios = append(report.Scenarios, observation)
	}
	report.Status = "verification_completed"
	return report, nil
}
