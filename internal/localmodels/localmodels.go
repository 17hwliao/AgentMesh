// Package localmodels discovers model catalogs exposed by local Ollama and
// LM Studio services. Discovery is read-only and restricted to loopback.
package localmodels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxDiscoveryBytes = 1 << 20

const (
	KindOllama   = "ollama"
	KindLMStudio = "lmstudio"
)

type Endpoint struct {
	Kind         string
	BaseURL      string
	DiscoveryURL string
	ChatBaseURL  string
}

type Source struct {
	Kind      string   `json:"kind"`
	BaseURL   string   `json:"base_url"`
	Models    []string `json:"models"`
	Reachable bool     `json:"reachable"`
	Error     string   `json:"error,omitempty"`
}

func DefaultEndpoint(kind string) string {
	switch kind {
	case KindOllama:
		return "http://127.0.0.1:11434"
	case KindLMStudio:
		return "http://127.0.0.1:1234/v1"
	default:
		return ""
	}
}

func Discover(ctx context.Context, kind, rawBaseURL string, client *http.Client) (Source, error) {
	endpoint, err := NormalizeEndpoint(kind, rawBaseURL)
	if err != nil {
		return Source{Kind: kind}, err
	}
	if client == nil {
		client = &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.DiscoveryURL, nil)
	if err != nil {
		return Source{Kind: kind, BaseURL: endpoint.BaseURL}, errors.New("local_models_endpoint_invalid")
	}
	response, err := client.Do(request)
	if err != nil {
		return Source{Kind: kind, BaseURL: endpoint.BaseURL}, errors.New("local_models_unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Source{Kind: kind, BaseURL: endpoint.BaseURL}, errors.New("local_models_status")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDiscoveryBytes+1))
	if err != nil {
		return Source{Kind: kind, BaseURL: endpoint.BaseURL}, errors.New("local_models_unreadable")
	}
	if len(body) > maxDiscoveryBytes {
		return Source{Kind: kind, BaseURL: endpoint.BaseURL}, errors.New("local_models_unreadable")
	}
	models, err := parseModels(kind, body)
	if err != nil {
		return Source{Kind: kind, BaseURL: endpoint.BaseURL}, err
	}
	return Source{Kind: kind, BaseURL: endpoint.BaseURL, Models: models, Reachable: true}, nil
}

func NormalizeEndpoint(kind, raw string) (Endpoint, error) {
	kind = strings.TrimSpace(kind)
	raw = strings.TrimSpace(raw)
	if kind != KindOllama && kind != KindLMStudio {
		return Endpoint{}, errors.New("local_models_kind_invalid")
	}
	if raw == "" {
		raw = DefaultEndpoint(kind)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "#") {
		return Endpoint{}, errors.New("local_models_endpoint_invalid")
	}
	if parsed.Scheme != "http" {
		return Endpoint{}, errors.New("local_models_endpoint_invalid")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
		if port := parsed.Port(); port != "" {
			parsed.Host = net.JoinHostPort(host, port)
		} else {
			parsed.Host = host
		}
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return Endpoint{}, errors.New("local_models_endpoint_must_be_loopback")
	}
	base := strings.TrimRight(parsed.String(), "/")
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if kind == KindOllama {
		base = strings.TrimSuffix(base, "/v1")
		path = strings.TrimSuffix(path, "/v1")
	} else {
		if path == "" {
			base += "/v1"
			path = "/v1"
		}
		if path != "/v1" {
			return Endpoint{}, errors.New("local_models_endpoint_invalid")
		}
	}
	if kind == KindOllama && path != "" {
		return Endpoint{}, errors.New("local_models_endpoint_invalid")
	}
	base = strings.TrimRight(base, "/")
	chat := base
	if kind == KindOllama {
		chat += "/v1"
	}
	discovery := base + "/api/tags"
	if kind == KindLMStudio {
		discovery = base + "/models"
	}
	return Endpoint{Kind: kind, BaseURL: base, DiscoveryURL: discovery, ChatBaseURL: chat}, nil
}

func parseModels(kind string, body []byte) ([]string, error) {
	seen := map[string]bool{}
	var values []string
	switch kind {
	case KindOllama:
		var payload struct {
			Models []struct {
				Name  string `json:"name"`
				Model string `json:"model"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, errors.New("local_models_invalid")
		}
		for _, model := range payload.Models {
			id := strings.TrimSpace(model.Name)
			if id == "" {
				id = strings.TrimSpace(model.Model)
			}
			if id != "" && !seen[id] {
				seen[id] = true
				values = append(values, id)
			}
		}
	case KindLMStudio:
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, errors.New("local_models_invalid")
		}
		for _, model := range payload.Data {
			id := strings.TrimSpace(model.ID)
			if id != "" && !seen[id] {
				seen[id] = true
				values = append(values, id)
			}
		}
	default:
		return nil, errors.New("local_models_kind_invalid")
	}
	sort.Strings(values)
	return values, nil
}
