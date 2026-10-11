// Package billing charges integer nanocurrency units (one yuan = 1e9 units).
// Prices and upstream model bindings are operator-controlled and versioned.
package billing

import (
	"agentmesh/internal/provider"
	"agentmesh/internal/providerconfig"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/bits"
	"regexp"
	"time"
)

const MaxMoney uint64 = 1 << 52
const MaxInputTokens = 65536
const MaxOutputTokens = 4096

var ErrInvalid = errors.New("billing_invalid")
var ErrInsufficient = errors.New("billing_insufficient_balance")
var ErrConflict = errors.New("billing_conflict")
var identifier = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type Price struct {
	Provider                  string  `json:"provider"`
	Model                     string  `json:"model"`
	UpstreamModel             string  `json:"upstream_model"`
	InputNanoPerMillion       uint64  `json:"input_nano_per_million"`
	OutputNanoPerMillion      uint64  `json:"output_nano_per_million"`
	CachedInputNanoPerMillion *uint64 `json:"cached_input_nano_per_million,omitempty"`
	MaxInput                  int     `json:"max_input_tokens"`
	Source                    string  `json:"source"`
}
type Catalog struct {
	Currency  string    `json:"currency"`
	ExpiresAt time.Time `json:"expires_at"`
	Prices    []Price   `json:"prices"`
}

func Decode(raw []byte) (Catalog, string, error) {
	var c Catalog
	if len(raw) > 64<<10 {
		return c, "", ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF || c.Currency != "CNY" || !c.ExpiresAt.After(time.Now()) || c.ExpiresAt.After(time.Now().Add(93*24*time.Hour)) || len(c.Prices) == 0 || len(c.Prices) > 96 {
		return c, "", ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range c.Prices {
		key := p.Provider + "/" + p.Model
		if !identifier.MatchString(p.Provider) || !providerconfig.AliasValid(p.Model) || !providerconfig.ModelValid(p.UpstreamModel) || seen[key] || p.MaxInput < 1 || p.MaxInput > MaxInputTokens || p.InputNanoPerMillion > 1e12 || p.OutputNanoPerMillion > 1e12 || len(p.Source) == 0 || len(p.Source) > 256 {
			return c, "", ErrInvalid
		}
		if p.CachedInputNanoPerMillion != nil && *p.CachedInputNanoPerMillion > p.InputNanoPerMillion {
			return c, "", ErrInvalid
		}
		seen[key] = true
	}
	canonical, _ := json.Marshal(c)
	h := sha256.Sum256(canonical)
	return c, hex.EncodeToString(h[:]), nil
}
func (c Catalog) Price(name, model string) (Price, bool) {
	for _, p := range c.Prices {
		if p.Provider == name && p.Model == model {
			return p, true
		}
	}
	return Price{}, false
}
func (p Price) Cost(u provider.Usage) (uint64, error) {
	if u.Estimated || u.InputTokens < 0 || u.InputTokens > p.MaxInput || u.OutputTokens < 0 || u.OutputTokens > MaxOutputTokens || u.CachedInputTokens < 0 || u.CachedInputTokens > u.InputTokens {
		return 0, ErrInvalid
	}
	cached := p.InputNanoPerMillion
	if p.CachedInputNanoPerMillion != nil {
		cached = *p.CachedInputNanoPerMillion
	}
	values := [][2]uint64{{uint64(u.InputTokens - u.CachedInputTokens), p.InputNanoPerMillion}, {uint64(u.CachedInputTokens), cached}, {uint64(u.OutputTokens), p.OutputNanoPerMillion}}
	var sum uint64
	for _, v := range values {
		hi, lo := bits.Mul64(v[0], v[1])
		if hi != 0 || sum > ^uint64(0)-lo {
			return 0, ErrInvalid
		}
		sum += lo
	}
	// Round once at nanocurrency precision, never floating-point cents.
	result := sum / 1000000
	if sum%1000000 != 0 {
		result++
	}
	if result > MaxMoney {
		return 0, ErrInvalid
	}
	return result, nil
}
