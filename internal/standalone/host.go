package standalone

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"agentmesh/internal/catalog"
	"agentmesh/internal/hostscan"
	"agentmesh/internal/localmodels"
)

type HostScanInput struct {
	ExtraRoots []string `json:"extra_roots,omitempty"`
}

type LocalModelScanInput struct {
	Kind    string `json:"kind,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

type LocalModelImportInput struct {
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	Slug    string `json:"slug,omitempty"`
	Name    string `json:"name,omitempty"`
	Confirm bool   `json:"confirm"`
}

type LocalModelImportResult struct {
	Provider catalog.Provider
	Models   []string
	Result   catalog.DiscoveryResult
}

func (a *App) ScanHostAgents(ctx context.Context, input HostScanInput) (hostscan.Result, error) {
	scanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return hostscan.New().Scan(scanCtx, hostscan.ScanOptions{ExtraRoots: input.ExtraRoots})
}

func (a *App) ScanLocalModels(ctx context.Context, input LocalModelScanInput) ([]localmodels.Source, error) {
	kinds := []string{strings.TrimSpace(input.Kind)}
	if kinds[0] == "" {
		kinds = []string{localmodels.KindOllama, localmodels.KindLMStudio}
	}
	sources := make([]localmodels.Source, 0, len(kinds))
	for _, kind := range kinds {
		source, err := localmodels.Discover(ctx, kind, input.BaseURL, nil)
		if err != nil {
			source.Error = err.Error()
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func (m *Manager) ImportLocalModels(ctx context.Context, input LocalModelImportInput) (LocalModelImportResult, error) {
	if !input.Confirm {
		return LocalModelImportResult{}, errors.New("local_models_import_requires_confirm")
	}
	kind := strings.TrimSpace(input.Kind)
	endpoint, err := localmodels.NormalizeEndpoint(kind, input.BaseURL)
	if err != nil {
		return LocalModelImportResult{}, err
	}
	source, err := localmodels.Discover(ctx, kind, endpoint.BaseURL, nil)
	if err != nil {
		return LocalModelImportResult{}, err
	}
	if len(source.Models) == 0 {
		return LocalModelImportResult{}, errors.New("local_models_empty")
	}
	slug := strings.TrimSpace(input.Slug)
	if slug == "" {
		slug = kind
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		switch kind {
		case localmodels.KindOllama:
			name = "Ollama (local)"
		case localmodels.KindLMStudio:
			name = "LM Studio (local)"
		}
	}
	now := time.Now().UTC()
	provider := catalog.Provider{
		ID: catalog.NewID("prv"), Slug: slug, Name: name, BaseURL: endpoint.ChatBaseURL,
		Kind: kind, AuthType: catalog.AuthNone, Enabled: true, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := validateProvider(provider, "", false); err != nil {
		return LocalModelImportResult{}, err
	}

	data, err := m.loadData(ctx)
	if err != nil {
		return LocalModelImportResult{}, err
	}
	var existing *catalog.Provider
	for index := range data.Providers {
		if data.Providers[index].Slug == slug {
			existing = &data.Providers[index]
			break
		}
	}
	if existing != nil {
		if existing.Kind != kind || existing.AuthType != catalog.AuthNone || strings.TrimRight(existing.BaseURL, "/") != endpoint.ChatBaseURL {
			return LocalModelImportResult{}, catalog.ErrConflict
		}
		provider = *existing
	}

	var result catalog.DiscoveryResult
	err = m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if existing == nil {
			if err := catalog.InsertProvider(ctx, tx, provider); err != nil {
				return err
			}
		}
		var upsertErr error
		result, upsertErr = catalog.UpsertDiscoveredModels(ctx, tx, provider.ID, provider.Slug, source.Models, now)
		return upsertErr
	})
	if err != nil {
		return LocalModelImportResult{}, err
	}
	return LocalModelImportResult{Provider: provider, Models: source.Models, Result: result}, nil
}
