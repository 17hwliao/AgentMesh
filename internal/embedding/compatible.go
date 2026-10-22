package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"agentmesh/internal/provider"
	"agentmesh/internal/providerconfig"
	"agentmesh/internal/reservation"
	"agentmesh/internal/telemetry"
)

// CompatibleConfig pins a server-owned model and dimension. A public alias
// never selects an arbitrary upstream model or changes its vector space.
type CompatibleConfig struct {
	BaseURL, APIKey, Model string
	Dimension              int
	Client                 *http.Client
}

type CompatibleProvider struct {
	baseURL, apiKey, model string
	dimension              int
	client                 *http.Client
}

func NewCompatibleProvider(cfg CompatibleConfig) (*CompatibleProvider, error) {
	base, err := providerconfig.BaseURL(cfg.BaseURL)
	if err != nil || !providerconfig.KeyValid(cfg.APIKey) || !providerconfig.ModelValid(cfg.Model) || cfg.Dimension < 1 || cfg.Dimension > MaxDimension {
		return nil, ErrUnavailable
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: telemetry.Transport{}}
	if cfg.Client != nil {
		copy := *cfg.Client
		client = &copy
	}
	// Do not forward credentials or paid requests through redirects.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &CompatibleProvider{base, cfg.APIKey, cfg.Model, cfg.Dimension, client}, nil
}

func (p *CompatibleProvider) Name() string { return "compatible" }
func (p *CompatibleProvider) BillingModel(alias string) string {
	if alias == "embedding" {
		return p.model
	}
	return ""
}

func (p *CompatibleProvider) Embed(ctx context.Context, alias string, inputs []string) ([][]float64, error) {
	v, _, err := p.EmbedMetered(ctx, alias, inputs, nil)
	return v, err
}

// Every paid upstream batch has its own durable attempt and usage receipt.
// A partial batch failure preserves all known receipts; unknown fees stay held.
func (p *CompatibleProvider) EmbedMetered(ctx context.Context, alias string, inputs []string, session reservation.StreamSession) ([][]float64, *provider.Usage, error) {
	if len(inputs) == 0 || len(inputs) > MaxInputs {
		return nil, nil, ErrInvalidRequest
	}
	for _, text := range inputs {
		if len(text) == 0 || len(text) > MaxInputBytes {
			return nil, nil, ErrInvalidRequest
		}
	}
	result := make([][]float64, 0, len(inputs))
	total := &provider.Usage{}
	// Bailian text embeddings accept at most 20 texts per upstream request.
	for start := 0; start < len(inputs); start += 20 {
		end := min(start+20, len(inputs))
		if session != nil {
			if err := session.BeforeAttempt(ctx, p.Name(), provider.ChatRequest{Model: alias}); err != nil {
				return nil, nil, err
			}
		}
		batch, usage, err := p.batch(ctx, inputs[start:end], session != nil)
		if session != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if usage != nil {
				if receiptErr := session.BeforeEmit(cleanup, p.Name(), provider.Chunk{Usage: usage}); receiptErr != nil {
					cancel()
					return nil, nil, receiptErr
				}
			}
			outcome := "succeeded"
			if err != nil {
				outcome = "failed"
			}
			finishErr := session.FinishAttempt(cleanup, p.Name(), outcome)
			cancel()
			if finishErr != nil {
				return nil, nil, finishErr
			}
		}
		if err != nil {
			return nil, nil, err
		}
		if usage != nil {
			total.InputTokens += usage.InputTokens
		}
		result = append(result, batch...)
	}
	return result, total, nil
}

func (p *CompatibleProvider) batch(ctx context.Context, inputs []string, requireUsage bool) ([][]float64, *provider.Usage, error) {
	body, _ := json.Marshal(struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions int      `json:"dimensions"`
		Encoding   string   `json:"encoding_format"`
	}{p.model, inputs, p.dimension, "float"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, nil, ErrProtocol
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, ErrUpstream
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, ErrUpstream
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return nil, nil, ErrProtocol
	}
	var data struct {
		Model string `json:"model"`
		Usage *struct {
			Prompt *int `json:"prompt_tokens"`
			Total  *int `json:"total_tokens"`
		} `json:"usage"`
		Data []struct {
			Index  int       `json:"index"`
			Vector []float64 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &data) != nil || (data.Model != "" && data.Model != p.model) {
		return nil, nil, ErrProtocol
	}
	var usage *provider.Usage
	if data.Usage != nil && data.Usage.Prompt != nil && data.Usage.Total != nil && *data.Usage.Prompt >= 0 && *data.Usage.Prompt <= 65536 && *data.Usage.Total == *data.Usage.Prompt {
		usage = &provider.Usage{InputTokens: *data.Usage.Prompt}
	}
	if requireUsage && usage == nil {
		return nil, nil, ErrProtocol
	}
	if len(data.Data) != len(inputs) {
		return nil, usage, ErrProtocol
	}
	ordered := make([][]float64, len(inputs))
	for _, row := range data.Data {
		if row.Index < 0 || row.Index >= len(inputs) || ordered[row.Index] != nil || len(row.Vector) != p.dimension {
			return nil, usage, ErrProtocol
		}
		ordered[row.Index] = row.Vector
	}
	if validateVectorsForRequest(ordered, len(inputs)) != nil {
		return nil, usage, ErrProtocol
	}
	return ordered, usage, nil
}
