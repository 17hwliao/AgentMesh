// Package catalog owns the persistent standalone catalog. Runtime request
// handling reads immutable snapshots built from this package; it never reads
// SQLite on the hot path.
package catalog

import "time"

const (
	ProviderOpenAICompatible = "openai_compatible"
	ProviderAgentMesh        = "agentmesh"
	ProviderOllama           = "ollama"
	ProviderLMStudio         = "lmstudio"

	AuthBearer = "bearer"
	AuthNone   = "none"
)

type Provider struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	BaseURL     string    `json:"base_url"`
	Kind        string    `json:"kind"`
	AuthType    string    `json:"auth_type"`
	TLSInsecure bool      `json:"tls_insecure"`
	Enabled     bool      `json:"enabled"`
	Revision    int       `json:"revision"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Credential struct {
	ID         string    `json:"id"`
	ProviderID string    `json:"provider_id"`
	KeyID      string    `json:"-"`
	Ciphertext []byte    `json:"-"`
	Nonce      []byte    `json:"-"`
	Mask       string    `json:"mask"`
	Version    int       `json:"version"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Model struct {
	ID              string    `json:"id"`
	ProviderID      string    `json:"provider_id"`
	UpstreamID      string    `json:"upstream_id"`
	PublicID        string    `json:"public_id"`
	Enabled         bool      `json:"enabled"`
	Stale           bool      `json:"stale"`
	Tools           bool      `json:"tools"`
	Vision          bool      `json:"vision"`
	MaxOutputTokens int       `json:"max_output_tokens"`
	Source          string    `json:"source"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ClientKey struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Prefix         string     `json:"prefix"`
	Hash           []byte     `json:"-"`
	Scopes         []string   `json:"scopes"`
	Enabled        bool       `json:"enabled"`
	RPM            int        `json:"rpm"`
	DailyRequests  int        `json:"daily_requests"`
	DailyTokens    int        `json:"daily_tokens"`
	ModelAllowlist []string   `json:"model_allowlist"`
	TenantID       string     `json:"tenant_id"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

type RoutingMember struct {
	Group    string `json:"group"`
	ModelID  string `json:"model_id"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Enabled  bool   `json:"enabled"`
}

type RoutingGroup struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Enabled     bool            `json:"enabled"`
	Members     []RoutingMember `json:"members"`
}

type SnapshotData struct {
	Generation    int64
	Providers     []Provider
	Credentials   map[string]Credential
	Models        []Model
	Keys          []ClientKey
	RoutingGroups []RoutingGroup
}

type UsageEvent struct {
	KeyID        string
	ProviderID   string
	Model        string
	InputTokens  int
	OutputTokens int
	Result       string
	CreatedAt    time.Time
}

type Cooldown struct {
	ProviderID string
	Model      string
	Until      time.Time
	Reason     string
}
