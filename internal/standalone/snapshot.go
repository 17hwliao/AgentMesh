package standalone

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentmesh/internal/catalog"
	"agentmesh/internal/provider"
	"agentmesh/internal/provider/ark"
	"agentmesh/internal/providerconfig"
	"agentmesh/internal/router"
	"agentmesh/internal/secrets"
)

type ProviderView struct {
	Catalog catalog.Provider
	Adapter provider.Provider
	Mask    string
	Error   string
}

type ModelView struct {
	Catalog  catalog.Model
	Provider ProviderView
}

type Snapshot struct {
	Generation  int64
	Providers   []ProviderView
	AllModels   []catalog.Model
	Models      map[string]ModelView
	ModelList   []string
	Keys        []catalog.ClientKey
	KeyByID     map[string]catalog.ClientKey
	KeyByPrefix map[string]catalog.ClientKey
	Groups      map[string]catalog.RoutingGroup
	GroupList   []string
}

type Manager struct {
	store   *catalog.Store
	keyring *secrets.Keyring

	mu      sync.Mutex
	current atomic.Pointer[Snapshot]
	repair  atomic.Value

	usageMu         sync.Mutex
	usage           map[string]*keyUsageWindow
	dailyTokenLimit int

	cooldownMu sync.RWMutex
	cooldowns  map[string]time.Time
}

func NewManager(store *catalog.Store, keyring *secrets.Keyring) *Manager {
	m := &Manager{
		store: store, keyring: keyring,
		usage: map[string]*keyUsageWindow{}, cooldowns: map[string]time.Time{},
	}
	m.repair.Store("")
	m.loadCooldowns(context.Background())
	m.loadUsageCounters(context.Background())
	return m
}

func (m *Manager) SetDailyTokenLimit(limit int) {
	m.dailyTokenLimit = limit
}

func (m *Manager) Snapshot() *Snapshot {
	return m.current.Load()
}

func (m *Manager) Repair() string {
	value, _ := m.repair.Load().(string)
	return value
}

func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := m.store.LoadData(ctx, m.store.DB)
	if err != nil {
		m.repair.Store("catalog_read_failed")
		return err
	}
	snapshot, err := BuildSnapshot(data, m.keyring)
	if err != nil {
		m.repair.Store("snapshot_invalid")
		return err
	}
	m.current.Store(snapshot)
	m.repair.Store("")
	return nil
}

func (m *Manager) mutate(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := catalog.IncrementGeneration(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	data, err := m.store.LoadData(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	snapshot, err := BuildSnapshot(data, m.keyring)
	if err != nil {
		_ = tx.Rollback()
		m.repair.Store("candidate_snapshot_invalid")
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	m.current.Store(snapshot)
	m.repair.Store("")
	return nil
}

func BuildSnapshot(data catalog.SnapshotData, keyring *secrets.Keyring) (*Snapshot, error) {
	snapshot := &Snapshot{
		Generation:  data.Generation,
		Models:      map[string]ModelView{},
		KeyByID:     map[string]catalog.ClientKey{},
		KeyByPrefix: map[string]catalog.ClientKey{},
		Groups:      map[string]catalog.RoutingGroup{},
	}
	providers := map[string]*ProviderView{}
	for _, p := range data.Providers {
		view := ProviderView{Catalog: p}
		if credential, ok := data.Credentials[p.ID]; ok {
			view.Mask = credential.Mask
		}
		providers[p.ID] = &view
		snapshot.Providers = append(snapshot.Providers, view)
	}

	for index := range snapshot.Providers {
		p := &snapshot.Providers[index]
		if _, err := providerconfig.BaseURL(p.Catalog.BaseURL); err != nil {
			return nil, fmt.Errorf("invalid provider %s base URL", p.Catalog.Slug)
		}
		if err := rejectMetadataHost(p.Catalog.BaseURL); err != nil {
			return nil, fmt.Errorf("forbidden provider %s base URL", p.Catalog.Slug)
		}
		if err := validateLocalProvider(p.Catalog); err != nil {
			return nil, fmt.Errorf("invalid local provider %s: %w", p.Catalog.Slug, err)
		}
		if !p.Catalog.Enabled {
			continue
		}
		models := modelsForProvider(data.Models, p.Catalog.ID)
		if len(models) == 0 {
			continue
		}
		credential, exists := data.Credentials[p.Catalog.ID]
		var apiKey string
		if p.Catalog.AuthType == catalog.AuthBearer {
			if !exists {
				p.Error = "credential_missing"
				continue
			}
			plain, err := keyring.Decrypt(credential.Ciphertext, credential.Nonce, credential.KeyID, credentialAAD(p.Catalog.ID, credential.ID, credential.Version))
			if err != nil {
				p.Error = "credential_unreadable"
				continue
			}
			apiKey = string(plain)
		}
		aliases := map[string]string{}
		var vision, tools []string
		publicToPrivate := map[string]string{}
		for index, model := range models {
			if model.PublicID == "" || model.UpstreamID == "" || !strings.HasPrefix(model.PublicID, p.Catalog.Slug+"/") {
				return nil, fmt.Errorf("invalid model %s for provider %s", model.ID, p.Catalog.Slug)
			}
			privateAlias := fmt.Sprintf("model_%d", index)
			aliases[privateAlias] = model.UpstreamID
			publicToPrivate[model.PublicID] = privateAlias
			if model.Vision {
				vision = append(vision, privateAlias)
			}
			if model.Tools {
				tools = append(tools, privateAlias)
			}
		}
		adapter, err := ark.New(ark.Config{
			BaseURL:      p.Catalog.BaseURL,
			APIKey:       apiKey,
			Name:         "compatible",
			Models:       aliases,
			NoAuth:       p.Catalog.AuthType == catalog.AuthNone,
			VisionModels: vision,
			ToolModels:   tools,
		}, providerHTTPClient(p.Catalog, 30*time.Second))
		if err != nil {
			return nil, fmt.Errorf("invalid provider %s", p.Catalog.Slug)
		}
		p.Adapter = routedAdapter{Provider: adapter, name: p.Catalog.Slug, publicToPrivate: publicToPrivate}
	}

	for index := range snapshot.Providers {
		p := snapshot.Providers[index]
		for _, model := range data.Models {
			if model.ProviderID != p.Catalog.ID || !model.Enabled || model.Stale || p.Adapter == nil {
				continue
			}
			view := ModelView{Catalog: model, Provider: p}
			snapshot.Models[model.PublicID] = view
			snapshot.ModelList = append(snapshot.ModelList, model.PublicID)
		}
	}
	sort.Strings(snapshot.ModelList)
	snapshot.AllModels = append([]catalog.Model(nil), data.Models...)
	for _, key := range data.Keys {
		snapshot.Keys = append(snapshot.Keys, key)
		snapshot.KeyByID[key.ID] = key
		snapshot.KeyByPrefix[key.Prefix] = key
	}
	for _, group := range data.RoutingGroups {
		if !group.Enabled {
			continue
		}
		snapshot.Groups[group.Name] = group
		snapshot.GroupList = append(snapshot.GroupList, group.Name)
	}
	sort.Strings(snapshot.GroupList)
	return snapshot, nil
}

func modelsForProvider(models []catalog.Model, providerID string) []catalog.Model {
	out := make([]catalog.Model, 0)
	for _, model := range models {
		if model.ProviderID == providerID && model.Enabled && !model.Stale {
			out = append(out, model)
		}
	}
	return out
}

type routedAdapter struct {
	provider.Provider
	name            string
	publicToPrivate map[string]string
}

func (a routedAdapter) Name() string { return a.name }

func (a routedAdapter) SupportsImages(alias string) bool {
	private, ok := a.publicToPrivate[alias]
	if !ok {
		return false
	}
	value, ok := a.Provider.(interface{ SupportsImages(string) bool })
	return ok && value.SupportsImages(private)
}

func (a routedAdapter) SupportsTools(alias string) bool {
	private, ok := a.publicToPrivate[alias]
	if !ok {
		return false
	}
	value, ok := a.Provider.(interface{ SupportsTools(string) bool })
	return ok && value.SupportsTools(private)
}

func (a routedAdapter) BillingModel(alias string) string {
	private, ok := a.publicToPrivate[alias]
	if !ok {
		return ""
	}
	value, ok := a.Provider.(interface{ BillingModel(string) string })
	if !ok {
		return ""
	}
	return value.BillingModel(private)
}

func (a routedAdapter) Stream(ctx context.Context, request provider.ChatRequest, emit provider.Emit) error {
	private, ok := a.publicToPrivate[request.Model]
	if !ok {
		return provider.ErrProtocol
	}
	request.Model = private
	return a.Provider.Stream(ctx, request, emit)
}

type runtimeProvider struct {
	provider.Provider
	manager    *Manager
	providerID string
	model      string
}

func (p *runtimeProvider) Stream(ctx context.Context, request provider.ChatRequest, emit provider.Emit) error {
	request.Model = p.model
	err := p.Provider.Stream(ctx, request, emit)
	if err != nil {
		p.manager.recordCooldown(p.providerID, p.model, err)
		return err
	}
	p.manager.clearCooldown(p.providerID, p.model)
	return nil
}

func (p *runtimeProvider) SupportsImages(alias string) bool {
	alias = p.model
	value, ok := p.Provider.(interface{ SupportsImages(string) bool })
	return ok && value.SupportsImages(alias)
}

func (p *runtimeProvider) SupportsTools(alias string) bool {
	alias = p.model
	value, ok := p.Provider.(interface{ SupportsTools(string) bool })
	return ok && value.SupportsTools(alias)
}

type attemptRecorder struct {
	next     router.Observer
	provider string
	usage    *provider.Usage
}

func (*attemptRecorder) RouterObserverNonBlocking() {}

func (r *attemptRecorder) Observe(event router.AttemptEvent) {
	if event.Provider != "" {
		r.provider = event.Provider
	}
	if event.Usage != nil {
		usage := *event.Usage
		r.usage = &usage
	}
	if r.next != nil {
		r.next.Observe(event)
	}
}

type trackedStreamer struct {
	inner    router.Streamer
	recorder *attemptRecorder
	manager  *Manager
	keyID    string
	model    string
}

func (s *trackedStreamer) Stream(ctx context.Context, request provider.ChatRequest, emit provider.Emit) error {
	var usage provider.Usage
	var sawUsage bool
	err := s.inner.Stream(ctx, request, func(chunk provider.Chunk) error {
		if chunk.Usage != nil {
			usage = *chunk.Usage
			sawUsage = true
		}
		return emit(chunk)
	})
	result := "success"
	if err != nil {
		result = "error"
	}
	inputTokens, outputTokens := 0, 0
	if sawUsage {
		inputTokens, outputTokens = usage.InputTokens, usage.OutputTokens
	}
	s.manager.RecordUsage(context.Background(), s.keyID, s.recorder.provider, s.model, inputTokens, outputTokens, result)
	s.manager.Audit(context.Background(), s.keyID, "data_request", s.model, result,
		fmt.Sprintf("provider=%s input_tokens=%d output_tokens=%d", s.recorder.provider, inputTokens, outputTokens))
	return err
}

func (s *trackedStreamer) SupportsInput(request provider.ChatRequest) bool {
	value, ok := s.inner.(interface {
		SupportsInput(provider.ChatRequest) bool
	})
	return ok && value.SupportsInput(request)
}

type Resolver struct {
	manager *Manager
}

func NewResolver(manager *Manager) *Resolver { return &Resolver{manager: manager} }

func (r *Resolver) Streamer(ctx context.Context, tenantID, model string) (router.Streamer, bool) {
	return r.StreamerWithObserver(ctx, tenantID, model, nil)
}

func (r *Resolver) StreamerWithObserver(ctx context.Context, tenantID, model string, observer router.Observer) (router.Streamer, bool) {
	snapshot := r.manager.Snapshot()
	if snapshot == nil || ctx.Err() != nil {
		return nil, false
	}
	key, ok := snapshot.KeyByID[tenantID]
	if !ok || !key.Enabled {
		return nil, false
	}
	if !modelAllowed(key, model) {
		return nil, false
	}
	models := snapshot.routeModels(model)
	if len(models) == 0 {
		return nil, false
	}
	providers := make([]provider.Provider, 0, len(models))
	for _, item := range models {
		if item.Provider.Adapter == nil || r.manager.cooldownActive(item.Provider.Catalog.ID, item.Catalog.PublicID) {
			continue
		}
		providers = append(providers, &runtimeProvider{
			Provider: item.Provider.Adapter, manager: r.manager,
			providerID: item.Provider.Catalog.ID, model: item.Catalog.PublicID,
		})
	}
	if len(providers) == 0 {
		return nil, false
	}
	recorder := &attemptRecorder{next: observer}
	return &trackedStreamer{
		inner: router.NewWithObserver(providers, recorder), recorder: recorder,
		manager: r.manager, keyID: tenantID, model: model,
	}, true
}

func (r *Resolver) Models(ctx context.Context, tenantID string) ([]string, error) {
	snapshot := r.manager.Snapshot()
	if snapshot == nil {
		return nil, errors.New("snapshot_unavailable")
	}
	key, ok := snapshot.KeyByID[tenantID]
	if !ok || !key.Enabled {
		return nil, errors.New("key_unavailable")
	}
	out := make([]string, 0, len(snapshot.ModelList)+len(snapshot.GroupList))
	for _, model := range snapshot.ModelList {
		if modelAllowed(key, model) {
			out = append(out, model)
		}
	}
	for _, group := range snapshot.GroupList {
		if modelAllowed(key, group) {
			out = append(out, group)
		}
	}
	return out, nil
}

func (r *Resolver) VisibleProviders(ctx context.Context, tenantID string) []provider.Provider {
	snapshot := r.manager.Snapshot()
	if snapshot == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []provider.Provider
	for _, p := range snapshot.Providers {
		if p.Adapter != nil && !seen[p.Catalog.ID] {
			seen[p.Catalog.ID] = true
			out = append(out, p.Adapter)
		}
	}
	return out
}

func (s *Snapshot) routeModels(model string) []ModelView {
	if view, ok := s.Models[model]; ok {
		return []ModelView{view}
	}
	group, ok := s.Groups[model]
	if !ok {
		return nil
	}
	members := append([]catalog.RoutingMember(nil), group.Members...)
	sort.SliceStable(members, func(i, j int) bool {
		if members[i].Priority != members[j].Priority {
			return members[i].Priority < members[j].Priority
		}
		return members[i].Weight > members[j].Weight
	})
	out := make([]ModelView, 0, len(members))
	for _, member := range members {
		if !member.Enabled {
			continue
		}
		for _, view := range s.Models {
			if view.Catalog.ID == member.ModelID && view.Provider.Adapter != nil {
				out = append(out, view)
				break
			}
		}
	}
	return out
}

func modelAllowed(key catalog.ClientKey, model string) bool {
	if len(key.ModelAllowlist) == 0 {
		return true
	}
	for _, allowed := range key.ModelAllowlist {
		if allowed == model || (strings.HasSuffix(allowed, "/*") && strings.HasPrefix(model, strings.TrimSuffix(allowed, "*"))) {
			return true
		}
	}
	return false
}

func credentialAAD(providerID, credentialID string, version int) string {
	return fmt.Sprintf("%s/%s/%d", providerID, credentialID, version)
}

func (m *Manager) authenticate(prefix string, digest []byte) (catalog.ClientKey, bool) {
	snapshot := m.Snapshot()
	if snapshot == nil {
		return catalog.ClientKey{}, false
	}
	key, ok := snapshot.KeyByPrefix[prefix]
	if !ok || !key.Enabled || len(key.Hash) != len(digest) {
		return catalog.ClientKey{}, false
	}
	var diff byte
	for index := range digest {
		diff |= key.Hash[index] ^ digest[index]
	}
	if diff != 0 {
		return catalog.ClientKey{}, false
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now()) {
		return catalog.ClientKey{}, false
	}
	return key, true
}
