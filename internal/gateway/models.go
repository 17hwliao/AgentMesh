package gateway

import (
	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSONError(w, 405, "method_not_allowed")
		return
	}
	id, ok := auth.TenantID(r.Context())
	if !ok {
		writeJSONError(w, 401, "auth_failed")
		return
	}
	catalog, ok := s.tenantResolver.(interface {
		Models(context.Context, string) ([]string, error)
	})
	if !ok {
		writeJSONError(w, 503, "models_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	aliases, e := catalog.Models(ctx, id)
	if e != nil {
		writeJSONError(w, 503, "models_unavailable")
		return
	}
	data := []any{}
	for _, alias := range aliases {
		embedding := alias == "embedding" && s.embeddings != nil
		route, allowed := s.tenantResolver.Streamer(ctx, id, alias)
		if !allowed && !embedding {
			continue
		}
		capabilities := map[string]any{"chat": !embedding, "embedding": embedding, "vision": false, "tools": false, "max_output_tokens": 4096, "input_budget_units": provider.MaxTextBytes}
		if c, ok := route.(interface {
			SupportsInput(provider.ChatRequest) bool
		}); ok && !embedding {
			capabilities["vision"] = c.SupportsInput(provider.ChatRequest{Model: alias, Messages: []provider.Message{{Role: "user", Parts: []provider.ContentPart{{Type: "image_url"}}}}})
			capabilities["tools"] = c.SupportsInput(provider.ChatRequest{Model: alias, Tools: []provider.ToolDefinition{{Type: "function"}}})
		}
		row := map[string]any{"id": alias, "object": "model", "created": 0, "owned_by": "agentmesh", "capabilities": capabilities}
		if selected := r.PathValue("model"); selected != "" {
			if selected == alias {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(row)
				return
			}
			continue
		}
		data = append(data, row)
	}
	if r.PathValue("model") != "" {
		writeJSONError(w, 404, "model_not_found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
