package standalone

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentmesh/internal/catalog"
	"agentmesh/internal/secrets"
)

type Config struct {
	DataDir             string
	DataAddr            string
	AdminAddr           string
	RotateLocalKey      bool
	ResetAdminToken     bool
	BackupDir           string
	RestoreDir          string
	MaxConcurrent       int
	IPRequestsPerMinute int
	MaxBodyBytes        int64
	DailyTokenLimit     int
	Stdout              io.Writer
	Stderr              io.Writer
}

type App struct {
	cfg        Config
	store      *catalog.Store
	keyring    *secrets.Keyring
	manager    *Manager
	resolver   *Resolver
	admin      *adminService
	adminToken string
	localKey   string
	dataURL    string
	adminURL   string

	dataServer  *http.Server
	adminServer *http.Server
	dataLn      net.Listener
	adminLn     net.Listener
	limiter     *dataLimiter
	dataLock    *os.File
	closeOnce   sync.Once
}

func Run(ctx context.Context, cfg Config) error {
	app, err := New(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close(context.Background())
	return app.Run(ctx)
}

func New(ctx context.Context, cfg Config) (*App, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("standalone_data_dir_required")
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.DataAddr == "" {
		cfg.DataAddr = "127.0.0.1:18080"
	}
	if cfg.AdminAddr == "" {
		cfg.AdminAddr = "127.0.0.1:18081"
	}
	if cfg.MaxConcurrent < 0 || cfg.IPRequestsPerMinute < 0 || cfg.MaxBodyBytes < 0 || cfg.DailyTokenLimit < 0 {
		return nil, errors.New("standalone_limit_invalid")
	}
	if err := validateLoopbackAddress(cfg.DataAddr); err != nil {
		return nil, err
	}
	if err := validateLoopbackAddress(cfg.AdminAddr); err != nil {
		return nil, err
	}
	dataLock, err := lockDataDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			_ = dataLock.Close()
		}
	}()
	if strings.TrimSpace(cfg.RestoreDir) != "" {
		if err := restoreDataDir(cfg.RestoreDir, cfg.DataDir); err != nil {
			return nil, err
		}
	}
	store, err := catalog.Open(filepath.Join(cfg.DataDir, "catalog.db"))
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	keyring, err := secrets.LoadOrCreate(filepath.Join(cfg.DataDir, "master.key"))
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	manager := NewManager(store, keyring)
	manager.SetDailyTokenLimit(cfg.DailyTokenLimit)
	if err := manager.Reload(ctx); err != nil {
		// A persisted bad catalog must leave the management surface available.
		// The data plane reports not-ready until an administrator repairs it.
		fmt.Fprintf(cfg.Stderr, "standalone repair mode: %s\n", manager.Repair())
	}
	adminToken, err := ensureAdminToken(ctx, store, cfg.ResetAdminToken)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	localKey, err := ensureLocalKey(ctx, store, manager, cfg.RotateLocalKey)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	opened = true
	if cfg.RotateLocalKey || cfg.ResetAdminToken {
		if localKey != "" {
			fmt.Fprintf(cfg.Stdout, "local API key (shown once): %s\n", localKey)
		}
		if adminToken != "" {
			fmt.Fprintf(cfg.Stdout, "admin token (shown once): %s\n", adminToken)
		}
		return &App{cfg: cfg, store: store, keyring: keyring, manager: manager, adminToken: adminToken, localKey: localKey, limiter: newDataLimiter(), dataLock: dataLock}, nil
	}
	app := &App{
		cfg:        cfg,
		store:      store,
		keyring:    keyring,
		manager:    manager,
		resolver:   NewResolver(manager),
		adminToken: adminToken,
		localKey:   localKey,
		limiter:    newDataLimiter(),
		dataLock:   dataLock,
	}
	app.admin = newAdminService(app)
	return app, nil
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("standalone_address_invalid")
	}
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return errors.New("standalone_address_must_be_loopback")
	}
	return nil
}

func (a *App) Manager() *Manager     { return a.manager }
func (a *App) Store() *catalog.Store { return a.store }
func (a *App) DataURL() string       { return a.dataURL }
func (a *App) AdminURL() string      { return a.adminURL }
func (a *App) AdminToken() string    { return a.adminToken }
func (a *App) LocalKeyOnce() string  { return a.takeLocalKeyOnce() }

func (a *App) Start() error {
	if a.dataServer != nil {
		return nil
	}
	dataLn, err := net.Listen("tcp", a.cfg.DataAddr)
	if err != nil {
		return err
	}
	adminLn, err := net.Listen("tcp", a.cfg.AdminAddr)
	if err != nil {
		_ = dataLn.Close()
		return err
	}
	a.dataLn, a.adminLn = dataLn, adminLn
	a.dataURL = "http://" + dataLn.Addr().String()
	a.adminURL = "http://" + adminLn.Addr().String()
	a.dataServer = &http.Server{Handler: a.DataHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	a.adminServer = &http.Server{Handler: a.AdminHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() { _ = a.dataServer.Serve(dataLn) }()
	go func() { _ = a.adminServer.Serve(adminLn) }()
	return nil
}

func (a *App) Run(ctx context.Context) error {
	if a.cfg.RotateLocalKey || a.cfg.ResetAdminToken {
		return a.Close(context.Background())
	}
	if strings.TrimSpace(a.cfg.BackupDir) != "" {
		if err := a.Backup(ctx, a.cfg.BackupDir); err != nil {
			return err
		}
		return a.Close(context.Background())
	}
	if a.localKey != "" {
		fmt.Fprintf(a.cfg.Stdout, "local API key (shown once): %s\n", a.localKey)
	}
	if a.adminToken != "" {
		fmt.Fprintf(a.cfg.Stdout, "admin token (shown once): %s\n", a.adminToken)
	}
	if err := a.Start(); err != nil {
		return err
	}
	fmt.Fprintf(a.cfg.Stdout, "data plane: %s\n", a.dataURL)
	fmt.Fprintf(a.cfg.Stdout, "admin plane: %s/admin/\n", a.adminURL)
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.Close(shutdown)
}

func (a *App) Close(ctx context.Context) error {
	var first error
	a.closeOnce.Do(func() {
		if a.dataServer != nil {
			if err := a.dataServer.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		if a.adminServer != nil {
			if err := a.adminServer.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		if a.store != nil {
			if err := a.store.Close(); err != nil && first == nil {
				first = err
			}
		}
		if a.dataLock != nil {
			if err := a.dataLock.Close(); err != nil && first == nil {
				first = err
			}
		}
	})
	return first
}

func ensureAdminToken(ctx context.Context, store *catalog.Store, reset bool) (string, error) {
	existing, err := store.GetMeta(ctx, catalog.MetaAdminTokenHash)
	if err != nil {
		return "", err
	}
	if existing != "" && !reset {
		return "", nil
	}
	token, err := randomToken("agm_admin_")
	if err != nil {
		return "", err
	}
	if err := store.SetMeta(ctx, catalog.MetaAdminTokenHash, hashString(token)); err != nil {
		return "", err
	}
	return token, nil
}

func ensureLocalKey(ctx context.Context, store *catalog.Store, manager *Manager, rotate bool) (string, error) {
	existingID, err := store.GetMeta(ctx, catalog.MetaLocalKeyID)
	if err != nil {
		return "", err
	}
	if existingID != "" && !rotate {
		// Keep repair mode bootable even when the persisted snapshot cannot be
		// compiled. Rotating the key remains an explicit maintenance action.
		return "", nil
	}
	plain, err := randomToken("agm_")
	if err != nil {
		return "", err
	}
	keyID := catalog.NewID("key")
	hash := sha256.Sum256([]byte(plain))
	now := time.Now().UTC()
	err = manager.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if rotate && existingID != "" {
			enabled := false
			if err := catalog.UpdateClientKey(ctx, tx, existingID, &enabled, nil, nil, nil, nil, nil, now); err != nil && !errors.Is(err, catalog.ErrNotFound) {
				return err
			}
		}
		if err := catalog.InsertClientKey(ctx, tx, catalog.ClientKey{
			ID: keyID, Name: "local", Prefix: plain[:8], Hash: hash[:],
			Scopes: []string{"chat.write", "models.read"}, Enabled: true,
			TenantID: keyID, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_metadata(key,value) VALUES(?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, catalog.MetaLocalKeyID, keyID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return plain, nil
}

func randomToken(prefix string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (a *App) adminTokenMatches(value string) bool {
	snapshot, err := a.store.GetMeta(context.Background(), catalog.MetaAdminTokenHash)
	if err != nil || snapshot == "" {
		return false
	}
	expected, err := hex.DecodeString(snapshot)
	if err != nil {
		return false
	}
	actual := sha256.Sum256([]byte(value))
	if len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare(expected, actual[:]) == 1
}

func (a *App) takeLocalKeyOnce() string {
	if a.admin == nil {
		return ""
	}
	return a.admin.takeLocalKeyOnce()
}
