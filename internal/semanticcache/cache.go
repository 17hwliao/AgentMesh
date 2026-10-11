// Package semanticcache implements explicitly opted-in, tenant-isolated chat
// reuse. A hit represents zero new provider tokens, not provider usage evidence.
package semanticcache

import (
	"agentmesh/internal/embedding"
	"agentmesh/internal/provider"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	redis "github.com/redis/go-redis/v9"
	"math"
	"regexp"
	"strings"
	"time"
)

type Cache struct {
	Redis                   *redis.Client
	Embedder                embedding.Provider
	EmbeddingModel, Version string
	Threshold               float64
	TTL                     time.Duration
}
type Entry struct {
	Namespace string    `json:"namespace"`
	Prompt    string    `json:"prompt"`
	Vector    []float64 `json:"vector"`
	Text      string    `json:"text"`
	Expires   time.Time `json:"expires"`
}
type Prepared struct {
	Tenant, Namespace, Prompt string
	Vector                    []float64
}

var namespacePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
var numbers = regexp.MustCompile(`[0-9]+(?:[.,][0-9]+)*`)

func (c *Cache) Prepare(ctx context.Context, tenant, namespace, route string, r provider.ChatRequest) (Prepared, error) {
	var result Prepared
	if c == nil || c.Redis == nil || c.Embedder == nil || c.Version == "" || route == "" || c.EmbeddingModel == "" || c.Threshold < .995 || c.Threshold > 1 || c.TTL < time.Minute || c.TTL > time.Hour || !namespacePattern.MatchString(namespace) || !namespacePattern.MatchString(tenant) || r.Temperature == nil || *r.Temperature != 0 || len(r.Messages) == 0 || len(r.Messages) > 2 {
		return result, errors.New("cache_ineligible")
	}
	user := r.Messages[len(r.Messages)-1]
	if user.Role != "user" || len(user.Content) == 0 || len(user.Content) > 4096 {
		return result, errors.New("cache_ineligible")
	}
	if len(r.Messages) == 2 && r.Messages[0].Role != "system" {
		return result, errors.New("cache_ineligible")
	}
	// The complete system message, model route/configuration, generation limit
	// and caller-owned document version are exact context boundaries.
	history := r.Messages[:len(r.Messages)-1]
	raw, _ := json.Marshal([]any{namespace, route, c.Version, c.EmbeddingModel, r.Model, r.MaxTokens, history})
	sum := sha256.Sum256(raw)
	vectors, e := c.Embedder.Embed(ctx, c.EmbeddingModel, []string{user.Content})
	if e != nil || len(vectors) != 1 || !validVector(vectors[0]) {
		return result, errors.New("cache_embedding_failed")
	}
	return Prepared{Tenant: tenant, Namespace: hex.EncodeToString(sum[:]), Prompt: user.Content, Vector: vectors[0]}, nil
}
func keys(tenant string) (string, string) {
	sum := sha256.Sum256([]byte(tenant))
	key := "agentmesh:semantic:{" + hex.EncodeToString(sum[:]) + "}"
	return key + ":data", key + ":order"
}
func (c *Cache) Get(ctx context.Context, p Prepared) (string, bool, error) {
	data, order := keys(p.Tenant)
	ids, e := c.Redis.ZRevRange(ctx, order, 0, 127).Result()
	if e != nil {
		return "", false, e
	}
	if len(ids) == 0 {
		return "", false, nil
	}
	raw, e := c.Redis.HMGet(ctx, data, ids...).Result()
	if e != nil {
		return "", false, e
	}
	best := c.Threshold
	value := ""
	for _, v := range raw {
		text, ok := v.(string)
		if !ok || len(text) > 128<<10 {
			continue
		}
		var entry Entry
		if json.Unmarshal([]byte(text), &entry) != nil || entry.Namespace != p.Namespace || !entry.Expires.After(time.Now()) || len(entry.Text) == 0 || len(entry.Text) > 64<<10 || !validVector(entry.Vector) {
			continue
		}
		if strings.Join(numbers.FindAllString(entry.Prompt, -1), "|") != strings.Join(numbers.FindAllString(p.Prompt, -1), "|") {
			continue
		}
		score := cosine(p.Vector, entry.Vector)
		if entry.Prompt == p.Prompt {
			score = 1
		}
		if score >= best {
			best = score
			value = entry.Text
		}
	}
	return value, value != "", nil
}
func (c *Cache) Put(ctx context.Context, p Prepared, text string) error {
	if p.Tenant == "" || p.Namespace == "" || len(text) == 0 || len(text) > 64<<10 || !validVector(p.Vector) {
		return errors.New("cache_invalid")
	}
	raw, e := json.Marshal(Entry{Namespace: p.Namespace, Prompt: p.Prompt, Vector: p.Vector, Text: text, Expires: time.Now().Add(c.TTL)})
	if e != nil || len(raw) > 128<<10 {
		return errors.New("cache_entry_limit")
	}
	h := sha256.Sum256([]byte(p.Namespace + "\x00" + p.Prompt))
	id := hex.EncodeToString(h[:])
	data, order := keys(p.Tenant)
	return c.Redis.Eval(ctx, putLua, []string{data, order}, id, string(raw), time.Now().UnixMilli(), int(c.TTL.Seconds())).Err()
}

const putLua = `redis.call('HSET',KEYS[1],ARGV[1],ARGV[2])
redis.call('ZADD',KEYS[2],ARGV[3],ARGV[1])
local old=redis.call('ZRANGE',KEYS[2],0,-129)
for _,id in ipairs(old) do redis.call('HDEL',KEYS[1],id);redis.call('ZREM',KEYS[2],id) end
redis.call('EXPIRE',KEYS[1],ARGV[4]);redis.call('EXPIRE',KEYS[2],ARGV[4]);return 1`

func validVector(v []float64) bool {
	if len(v) == 0 || len(v) > 4096 {
		return false
	}
	norm := 0.
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
		norm += x * x
	}
	return norm > 0 && !math.IsInf(norm, 0)
}
func cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return -1
	}
	dot, na, nb := 0., 0., 0.
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return -1
	}
	return dot / math.Sqrt(na*nb)
}
