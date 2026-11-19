package standalone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"agentmesh/internal/catalog"
	"agentmesh/internal/localmodels"
	"agentmesh/internal/providerconfig"
)

type ProviderInput struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	Kind        string `json:"kind"`
	AuthType    string `json:"auth_type"`
	APIKey      string `json:"api_key"`
	TLSInsecure *bool  `json:"tls_insecure,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	Revision    *int   `json:"revision,omitempty"`
}

type ModelInput struct {
	UpstreamID      string `json:"upstream_id"`
	PublicID        string `json:"public_id"`
	Enabled         *bool  `json:"enabled,omitempty"`
	Tools           *bool  `json:"tools,omitempty"`
	Vision          *bool  `json:"vision,omitempty"`
	MaxOutputTokens *int   `json:"max_output_tokens,omitempty"`
}

type ClientKeyInput struct {
	Name           string     `json:"name"`
	Enabled        *bool      `json:"enabled,omitempty"`
	RPM            *int       `json:"rpm,omitempty"`
	DailyRequests  *int       `json:"daily_requests,omitempty"`
	DailyTokens    *int       `json:"daily_tokens,omitempty"`
	ModelAllowlist []string   `json:"model_allowlist,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

func (m *Manager) loadData(ctx context.Context) (catalog.SnapshotData, error) {
	return m.store.LoadData(ctx, m.store.DB)
}

func (m *Manager) CreateProvider(ctx context.Context, input ProviderInput) (catalog.Provider, error) {
	now := time.Now().UTC()
	p, credential, err := m.providerRecord(input, now, "", 1)
	if err != nil {
		return catalog.Provider{}, err
	}
	err = m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := catalog.InsertProvider(ctx, tx, p); err != nil {
			return err
		}
		if credential != nil {
			if err := catalog.InsertCredential(ctx, tx, *credential); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return catalog.Provider{}, err
	}
	return p, nil
}

func (m *Manager) UpdateProvider(ctx context.Context, id string, input ProviderInput) (catalog.Provider, error) {
	data, err := m.loadData(ctx)
	if err != nil {
		return catalog.Provider{}, err
	}
	var current catalog.Provider
	found := false
	for _, p := range data.Providers {
		if p.ID == id {
			current, found = p, true
			break
		}
	}
	if !found {
		return catalog.Provider{}, catalog.ErrNotFound
	}
	if input.Slug != "" && input.Slug != current.Slug {
		return catalog.Provider{}, errors.New("provider_slug_immutable")
	}
	if input.Name != "" {
		current.Name = input.Name
	}
	if input.BaseURL != "" {
		current.BaseURL = input.BaseURL
	}
	if input.Kind != "" {
		current.Kind = input.Kind
	}
	if input.AuthType != "" {
		current.AuthType = input.AuthType
	}
	if input.TLSInsecure != nil {
		current.TLSInsecure = *input.TLSInsecure
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	current.UpdatedAt = time.Now().UTC()
	current.Revision++
	if err := validateProvider(current, input.APIKey, m.hasCredential(data, id)); err != nil {
		return catalog.Provider{}, err
	}
	var rotated *catalog.Credential
	if input.APIKey != "" {
		credential, err := m.encryptCredential(current.ID, data.Credentials[current.ID], input.APIKey, current.UpdatedAt)
		if err != nil {
			return catalog.Provider{}, err
		}
		rotated = &credential
	}
	err = m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := catalog.UpdateProvider(ctx, tx, current); err != nil {
			return err
		}
		if rotated != nil {
			if existing, ok := data.Credentials[current.ID]; ok {
				rotated.ID = existing.ID
				rotated.CreatedAt = existing.CreatedAt
				if err := catalog.UpdateCredential(ctx, tx, *rotated); err != nil {
					return err
				}
			} else if err := catalog.InsertCredential(ctx, tx, *rotated); err != nil {
				return err
			}
		}
		return nil
	})
	return current, err
}

func (m *Manager) DeleteProvider(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return catalog.SoftDeleteProvider(ctx, tx, id, now)
	})
}

func (m *Manager) CreateModel(ctx context.Context, providerID string, input ModelInput) (catalog.Model, error) {
	data, err := m.loadData(ctx)
	if err != nil {
		return catalog.Model{}, err
	}
	var provider catalog.Provider
	found := false
	for _, p := range data.Providers {
		if p.ID == providerID {
			provider, found = p, true
			break
		}
	}
	if !found {
		return catalog.Model{}, catalog.ErrNotFound
	}
	upstream := strings.TrimSpace(input.UpstreamID)
	if upstream == "" {
		return catalog.Model{}, errors.New("model_upstream_required")
	}
	publicID := strings.TrimSpace(input.PublicID)
	if publicID == "" {
		publicID = provider.Slug + "/" + upstream
	}
	if !strings.HasPrefix(publicID, provider.Slug+"/") {
		return catalog.Model{}, errors.New("model_public_id_must_use_provider_slug")
	}
	now := time.Now().UTC()
	model := catalog.Model{
		ID: catalog.NewID("mdl"), ProviderID: provider.ID, UpstreamID: upstream, PublicID: publicID,
		Enabled: false, MaxOutputTokens: catalog.DefaultModelOutputTokens, Source: "manual", CreatedAt: now, UpdatedAt: now,
	}
	if input.Enabled != nil {
		model.Enabled = *input.Enabled
	}
	if input.Tools != nil {
		model.Tools = *input.Tools
	}
	if input.Vision != nil {
		model.Vision = *input.Vision
	}
	if input.MaxOutputTokens != nil {
		model.MaxOutputTokens = *input.MaxOutputTokens
	}
	err = m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return catalog.InsertModel(ctx, tx, model)
	})
	return model, err
}

func (m *Manager) UpdateModel(ctx context.Context, id string, input ModelInput) error {
	now := time.Now().UTC()
	return m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := catalog.SetModelState(ctx, tx, id, input.Enabled, input.Tools, input.Vision, input.MaxOutputTokens, now); err != nil {
			return err
		}
		return nil
	})
}

type DiscoveryResponse struct {
	Models []string                `json:"models"`
	Result catalog.DiscoveryResult `json:"result"`
}

func (m *Manager) DiscoverModels(ctx context.Context, providerID string) (DiscoveryResponse, error) {
	provider, apiKey, err := m.providerMaterial(ctx, providerID)
	if err != nil {
		return DiscoveryResponse{}, err
	}
	models, err := listUpstreamModels(ctx, provider, apiKey)
	if err != nil {
		return DiscoveryResponse{}, err
	}
	var result catalog.DiscoveryResult
	err = m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var e error
		result, e = catalog.UpsertDiscoveredModels(ctx, tx, provider.ID, provider.Slug, models, time.Now().UTC())
		return e
	})
	return DiscoveryResponse{Models: models, Result: result}, err
}

type ProviderTestResult struct {
	OK        bool   `json:"ok"`
	Kind      string `json:"kind"`
	Status    int    `json:"status"`
	Models    int    `json:"models,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

func (m *Manager) TestProvider(ctx context.Context, providerID, kind, modelName string, confirm bool) (ProviderTestResult, error) {
	provider, apiKey, err := m.providerMaterial(ctx, providerID)
	if err != nil {
		return ProviderTestResult{}, err
	}
	start := time.Now()
	switch kind {
	case "models", "":
		models, err := listUpstreamModels(ctx, provider, apiKey)
		result := ProviderTestResult{Kind: "models", Status: 200, OK: err == nil, Models: len(models), LatencyMS: time.Since(start).Milliseconds()}
		if err != nil {
			result.Status = 502
		}
		return result, err
	case "chat":
		if !confirm {
			return ProviderTestResult{}, errors.New("chat_test_requires_confirm")
		}
		if strings.TrimSpace(modelName) == "" {
			models, e := listUpstreamModels(ctx, provider, apiKey)
			if e != nil || len(models) == 0 {
				return ProviderTestResult{}, errors.New("chat_test_model_required")
			}
			modelName = models[0]
		}
		upstream, _ := m.modelForTest(ctx, providerID, modelName)
		status, err := testChat(ctx, provider, apiKey, upstream)
		result := ProviderTestResult{Kind: "chat", Status: status, OK: err == nil, LatencyMS: time.Since(start).Milliseconds()}
		return result, err
	default:
		return ProviderTestResult{}, errors.New("invalid_test_kind")
	}
}

func (m *Manager) CreateClientKey(ctx context.Context, input ClientKeyInput) (catalog.ClientKey, string, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return catalog.ClientKey{}, "", errors.New("client_key_name_required")
	}
	plain, err := randomToken("agm_")
	if err != nil {
		return catalog.ClientKey{}, "", err
	}
	hash := sha256.Sum256([]byte(plain))
	now := time.Now().UTC()
	key := catalog.ClientKey{
		ID: catalog.NewID("key"), Name: name, Prefix: plain[:8], Hash: hash[:],
		Scopes: []string{"chat.write", "models.read"}, Enabled: true, TenantID: "",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: input.ExpiresAt,
	}
	key.TenantID = key.ID
	if input.Enabled != nil {
		key.Enabled = *input.Enabled
	}
	if input.RPM != nil {
		key.RPM = *input.RPM
	}
	if input.DailyRequests != nil {
		key.DailyRequests = *input.DailyRequests
	}
	if input.DailyTokens != nil {
		key.DailyTokens = *input.DailyTokens
	}
	if input.ModelAllowlist != nil {
		key.ModelAllowlist = append([]string(nil), input.ModelAllowlist...)
	}
	err = m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return catalog.InsertClientKey(ctx, tx, key)
	})
	return key, plain, err
}

func (m *Manager) UpdateClientKey(ctx context.Context, id string, input ClientKeyInput) error {
	now := time.Now().UTC()
	var allowlist *[]string
	if input.ModelAllowlist != nil {
		allowlist = &input.ModelAllowlist
	}
	return m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return catalog.UpdateClientKey(ctx, tx, id, input.Enabled, input.RPM, input.DailyRequests, input.DailyTokens, allowlist, input.ExpiresAt, now)
	})
}

func (m *Manager) DeleteClientKey(ctx context.Context, id string) error {
	enabled := false
	return m.UpdateClientKey(ctx, id, ClientKeyInput{Enabled: &enabled})
}

func (m *Manager) RotateClientKey(ctx context.Context, id string) (catalog.ClientKey, string, error) {
	data, err := m.loadData(ctx)
	if err != nil {
		return catalog.ClientKey{}, "", err
	}
	var current catalog.ClientKey
	found := false
	for _, key := range data.Keys {
		if key.ID == id {
			current, found = key, true
			break
		}
	}
	if !found {
		return catalog.ClientKey{}, "", catalog.ErrNotFound
	}
	plain, err := randomToken("agm_")
	if err != nil {
		return catalog.ClientKey{}, "", err
	}
	hash := sha256.Sum256([]byte(plain))
	now := time.Now().UTC()
	next := current
	next.ID = catalog.NewID("key")
	next.Name = current.Name
	next.Prefix = plain[:8]
	next.Hash = hash[:]
	next.TenantID = next.ID
	next.CreatedAt = now
	next.UpdatedAt = now
	next.LastUsedAt = nil
	err = m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		disabled := false
		if err := catalog.UpdateClientKey(ctx, tx, current.ID, &disabled, nil, nil, nil, nil, nil, now); err != nil {
			return err
		}
		return catalog.InsertClientKey(ctx, tx, next)
	})
	return next, plain, err
}

func (m *Manager) TouchClientKey(ctx context.Context, id string) {
	_, _ = m.store.DB.ExecContext(ctx, `UPDATE client_keys SET last_used_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
}

func (m *Manager) ListData(ctx context.Context) (catalog.SnapshotData, error) {
	return m.loadData(ctx)
}

func (m *Manager) providerRecord(input ProviderInput, now time.Time, id string, revision int) (catalog.Provider, *catalog.Credential, error) {
	p := catalog.Provider{
		ID: id, Slug: strings.TrimSpace(input.Slug), Name: strings.TrimSpace(input.Name),
		BaseURL: strings.TrimSpace(input.BaseURL), Kind: input.Kind, AuthType: input.AuthType,
		Enabled: true, Revision: revision, CreatedAt: now, UpdatedAt: now,
	}
	if p.ID == "" {
		p.ID = catalog.NewID("prv")
	}
	if p.Kind == "" {
		p.Kind = catalog.ProviderOpenAICompatible
	}
	if p.AuthType == "" {
		p.AuthType = catalog.AuthBearer
	}
	if input.Enabled != nil {
		p.Enabled = *input.Enabled
	}
	if input.TLSInsecure != nil {
		p.TLSInsecure = *input.TLSInsecure
	}
	if err := validateProvider(p, input.APIKey, false); err != nil {
		return catalog.Provider{}, nil, err
	}
	if p.AuthType == catalog.AuthNone {
		return p, nil, nil
	}
	credential, err := m.encryptCredential(p.ID, catalog.Credential{}, input.APIKey, now)
	if err != nil {
		return catalog.Provider{}, nil, err
	}
	return p, &credential, nil
}

func (m *Manager) encryptCredential(providerID string, existing catalog.Credential, apiKey string, now time.Time) (catalog.Credential, error) {
	if strings.TrimSpace(apiKey) == "" {
		return catalog.Credential{}, errors.New("provider_api_key_required")
	}
	credentialID := existing.ID
	if credentialID == "" {
		credentialID = catalog.NewID("cred")
	}
	version := existing.Version + 1
	if version <= 0 {
		version = 1
	}
	ciphertext, nonce, keyID, err := m.keyring.Encrypt([]byte(apiKey), credentialAAD(providerID, credentialID, version))
	if err != nil {
		return catalog.Credential{}, err
	}
	mask := "****"
	if len(apiKey) >= 4 {
		mask += apiKey[len(apiKey)-4:]
	}
	return catalog.Credential{
		ID: credentialID, ProviderID: providerID, KeyID: keyID, Ciphertext: ciphertext, Nonce: nonce,
		Mask: mask, Version: version, Active: true, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (m *Manager) hasCredential(data catalog.SnapshotData, providerID string) bool {
	_, ok := data.Credentials[providerID]
	return ok
}

func (m *Manager) providerMaterial(ctx context.Context, providerID string) (catalog.Provider, string, error) {
	data, err := m.loadData(ctx)
	if err != nil {
		return catalog.Provider{}, "", err
	}
	var provider catalog.Provider
	found := false
	for _, p := range data.Providers {
		if p.ID == providerID {
			provider, found = p, true
			break
		}
	}
	if !found {
		return catalog.Provider{}, "", catalog.ErrNotFound
	}
	if provider.AuthType == catalog.AuthNone {
		return provider, "", nil
	}
	credential, ok := data.Credentials[providerID]
	if !ok {
		return catalog.Provider{}, "", errors.New("provider_credential_missing")
	}
	plain, err := m.keyring.Decrypt(credential.Ciphertext, credential.Nonce, credential.KeyID, credentialAAD(provider.ID, credential.ID, credential.Version))
	if err != nil {
		return catalog.Provider{}, "", errors.New("provider_credential_unreadable")
	}
	return provider, string(plain), nil
}

func validateProvider(p catalog.Provider, apiKey string, hasCredential bool) error {
	if !slugPattern.MatchString(p.Slug) {
		return errors.New("provider_slug_invalid")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 {
		return errors.New("provider_name_invalid")
	}
	if p.Kind != catalog.ProviderOpenAICompatible && p.Kind != catalog.ProviderAgentMesh &&
		p.Kind != catalog.ProviderOllama && p.Kind != catalog.ProviderLMStudio {
		return errors.New("provider_kind_invalid")
	}
	if p.AuthType != catalog.AuthBearer && p.AuthType != catalog.AuthNone {
		return errors.New("provider_auth_type_invalid")
	}
	base, err := providerconfig.BaseURL(p.BaseURL)
	if err != nil {
		return errors.New("provider_base_url_invalid")
	}
	if err := rejectMetadataHost(base); err != nil {
		return err
	}
	if err := validateLocalProvider(p); err != nil {
		return err
	}
	if isLocalModelProvider(p.Kind) && apiKey != "" {
		return errors.New("local_models_auth_invalid")
	}
	if p.AuthType == catalog.AuthBearer && !hasCredential && strings.TrimSpace(apiKey) == "" {
		return errors.New("provider_api_key_required")
	}
	return nil
}

func isLocalModelProvider(kind string) bool {
	return kind == catalog.ProviderOllama || kind == catalog.ProviderLMStudio
}

func validateLocalProvider(p catalog.Provider) error {
	if !isLocalModelProvider(p.Kind) {
		return nil
	}
	if p.AuthType != catalog.AuthNone || p.TLSInsecure {
		return errors.New("local_models_auth_invalid")
	}
	endpoint, err := localmodels.NormalizeEndpoint(p.Kind, p.BaseURL)
	if err != nil {
		return err
	}
	if endpoint.ChatBaseURL != strings.TrimRight(p.BaseURL, "/") {
		return errors.New("local_models_endpoint_invalid")
	}
	return nil
}

func rejectMetadataHost(base string) error {
	parsed, err := url.Parse(base)
	if err != nil {
		return errors.New("provider_base_url_invalid")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "metadata.google.internal" || host == "metadata" || host == "100.100.100.200" {
		return errors.New("provider_base_url_forbidden")
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLinkLocalUnicast() {
		return errors.New("provider_base_url_forbidden")
	}
	return nil
}

func listUpstreamModels(ctx context.Context, provider catalog.Provider, apiKey string) ([]string, error) {
	if isLocalModelProvider(provider.Kind) {
		source, err := localmodels.Discover(ctx, provider.Kind, provider.BaseURL, nil)
		if err != nil {
			return nil, err
		}
		if len(source.Models) == 0 {
			return nil, errors.New("local_models_empty")
		}
		return source.Models, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(provider.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if provider.AuthType == catalog.AuthBearer {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := providerHTTPClient(provider, 5*time.Second)
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("provider_models_unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("provider_models_status_%d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, errors.New("provider_models_unreadable")
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("provider_models_invalid")
	}
	out := make([]string, 0, len(payload.Data))
	seen := map[string]bool{}
	for _, model := range payload.Data {
		if model.ID != "" && !seen[model.ID] {
			seen[model.ID] = true
			out = append(out, model.ID)
		}
	}
	return out, nil
}

func testChat(ctx context.Context, provider catalog.Provider, apiKey, modelName string) (int, error) {
	body, _ := json.Marshal(map[string]any{
		"model": modelName, "stream": false, "max_tokens": 16,
		"messages": []map[string]string{{"role": "user", "content": "Reply with OK."}},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	if provider.AuthType == catalog.AuthBearer {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := providerHTTPClient(provider, 20*time.Second)
	response, err := client.Do(request)
	if err != nil {
		return 0, errors.New("provider_chat_unreachable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("provider_chat_status_%d", response.StatusCode)
	}
	return response.StatusCode, nil
}

func (m *Manager) modelForTest(ctx context.Context, providerID, modelName string) (string, error) {
	data, err := m.loadData(ctx)
	if err != nil {
		return "", err
	}
	for _, model := range data.Models {
		if model.ProviderID == providerID && (model.UpstreamID == modelName || model.PublicID == modelName) {
			return model.UpstreamID, nil
		}
	}
	return modelName, nil
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
