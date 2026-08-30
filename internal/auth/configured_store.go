package auth

import (
	"crypto/sha256"
	"strings"

	"agentmesh/internal/tenant"
)

const (
	authStoreEnvironment    = "AGENTMESH_AUTH_STORE"
	authMySQLDSNEnvironment = "AGENTMESH_AUTH_MYSQL_DSN"
	adminTokenEnvironment   = "AGENTMESH_ADMIN_TOKEN"
	authStoreMySQL          = "mysql"
)

type persistentRuntimeOpener func(string) (tenant.Store, tenant.Lifecycle, func(), error)

// ConfiguredRuntime keeps the MySQL database ownership and the derived admin
// credential together. The raw environment token is never retained.
type ConfiguredRuntime struct {
	Store          tenant.Store
	Lifecycle      tenant.Lifecycle
	AdminTokenHash [sha256.Size]byte
	cleanup        func()
}

func (r *ConfiguredRuntime) Close() {
	if r != nil && r.cleanup != nil {
		r.cleanup()
	}
}

// OpenConfiguredRuntime is the cmd/api entry point. The default remains the
// offline bootstrap store; mysql is an explicit, fail-closed opt-in.
func OpenConfiguredRuntime(lookup func(string) string) (*ConfiguredRuntime, error) {
	return openConfiguredRuntime(lookup, func(value string) (tenant.Store, tenant.Lifecycle, func(), error) {
		store, db, err := tenant.OpenMySQLStore(value)
		if err != nil {
			return nil, nil, func() {}, err
		}
		return store, tenant.NewMySQLLifecycle(db), func() { _ = db.Close() }, nil
	})
}

func openConfiguredRuntime(lookup func(string) string, open persistentRuntimeOpener) (*ConfiguredRuntime, error) {
	if lookup == nil || open == nil {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	mode := strings.TrimSpace(lookup(authStoreEnvironment))
	if mode == "" {
		store, err := Bootstrap(lookup)
		if err != nil {
			return nil, err
		}
		return &ConfiguredRuntime{Store: store, cleanup: func() {}}, nil
	}
	if mode != authStoreMySQL {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	dsn := strings.TrimSpace(lookup(authMySQLDSNEnvironment))
	adminToken := strings.TrimSpace(lookup(adminTokenEnvironment))
	if dsn == "" || adminToken == "" {
		return nil, &ConfigurationError{CodeConfigurationMissing}
	}
	store, lifecycle, cleanup, err := open(dsn)
	if err != nil || store == nil || lifecycle == nil {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	return &ConfiguredRuntime{
		Store:          store,
		Lifecycle:      lifecycle,
		AdminTokenHash: sha256.Sum256([]byte(adminToken)),
		cleanup:        cleanup,
	}, nil
}

// OpenConfiguredStore retains the offline Bootstrap mode unless mysql is
// explicitly selected. Missing persistent settings are rejected before an
// opener can allocate a connection or contact a database.
func OpenConfiguredStore(lookup func(string) string) (tenant.Store, func(), error) {
	runtime, err := OpenConfiguredRuntime(lookup)
	if err != nil {
		return nil, func() {}, err
	}
	return runtime.Store, runtime.Close, nil
}
