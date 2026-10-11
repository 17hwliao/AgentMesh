package billing

import (
	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Store) VerifyProviders(providers []provider.Provider) error {
	for _, p := range s.Catalog.Prices {
		found := false
		for _, adapter := range providers {
			if adapter.Name() != p.Provider {
				continue
			}
			bound, ok := adapter.(interface{ BillingModel(string) string })
			if !ok || bound.BillingModel(p.Model) != p.UpstreamModel {
				return ErrInvalid
			}
			if remote, ok := adapter.(interface {
				ValidateBilling(context.Context, string, string) error
			}); ok {
				if remote.ValidateBilling(context.Background(), p.Model, p.UpstreamModel) != nil {
					return ErrInvalid
				}
			}
			found = true
		}
		if !found {
			return ErrInvalid
		}
	}
	return nil
}
func (s *Store) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := auth.TenantID(r.Context())
		if !ok {
			respond(w, 401, map[string]string{"error": "auth_failed"})
			return
		}
		if r.Method != "GET" {
			respond(w, 405, map[string]string{"error": "method_not_allowed"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/v1/billing/balance":
			var balance, held uint64
			e := s.DB.QueryRowContext(ctx, "SELECT balance_nano,held_nano FROM billing_wallets WHERE tenant_id=? AND currency=?", tenant, s.Catalog.Currency).Scan(&balance, &held)
			if e != nil || held > balance {
				respond(w, 503, map[string]string{"error": "billing_unavailable"})
				return
			}
			respond(w, 200, map[string]any{"currency": s.Catalog.Currency, "scale": 1000000000, "balance_nano": balance, "held_nano": held, "available_nano": balance - held, "price_version": s.Version})
		case "/v1/billing/prices":
			respond(w, 200, map[string]any{"price_version": s.Version, "catalog": s.Catalog})
		case "/v1/billing/requests":
			rows, e := s.DB.QueryContext(ctx, "SELECT request_id,model,price_version,state,reserved_nano,charged_nano,input_tokens,output_tokens FROM billing_requests WHERE tenant_id=? AND currency=? ORDER BY created_at DESC LIMIT 100", tenant, s.Catalog.Currency)
			if e != nil {
				respond(w, 503, map[string]string{"error": "billing_unavailable"})
				return
			}
			defer rows.Close()
			result := []map[string]any{}
			for rows.Next() {
				var id, model, version, state string
				var reserve, charge, input, output uint64
				if rows.Scan(&id, &model, &version, &state, &reserve, &charge, &input, &output) != nil {
					respond(w, 503, map[string]string{"error": "billing_unavailable"})
					return
				}
				result = append(result, map[string]any{"request_id": id, "model": model, "price_version": version, "state": state, "reserved_nano": reserve, "charged_nano": charge, "input_tokens": input, "output_tokens": output})
			}
			if rows.Err() != nil {
				respond(w, 503, map[string]string{"error": "billing_unavailable"})
				return
			}
			respond(w, 200, result)
		default:
			respond(w, 404, map[string]string{"error": "not_found"})
		}
	})
}
func (s *Store) Admin(token string) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		var supplied [32]byte
		if len(parts) == 2 && parts[0] == "Bearer" {
			supplied = sha256.Sum256([]byte(parts[1]))
		}
		if len(token) < 32 || subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
			respond(w, 401, map[string]string{"error": "admin_auth_failed"})
			return
		}
		if r.Method != "POST" {
			respond(w, 405, map[string]string{"error": "method_not_allowed"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var e error
		switch r.URL.Path {
		case "/admin/billing/credits":
			var v struct {
				Tenant string `json:"tenant_id"`
				ID     string `json:"credit_id"`
				Proof  string `json:"proof"`
				Amount uint64 `json:"amount_nano"`
			}
			if d.Decode(&v) != nil || d.Decode(&struct{}{}) != io.EOF {
				e = ErrInvalid
			} else {
				e = s.Credit(ctx, v.Tenant, v.ID, v.Proof, v.Amount)
			}
		case "/admin/billing/resolve":
			var v struct {
				ID       string       `json:"request_id"`
				Proof    string       `json:"proof"`
				Attempts []Resolution `json:"attempts"`
			}
			if d.Decode(&v) != nil || d.Decode(&struct{}{}) != io.EOF {
				e = ErrInvalid
			} else {
				e = s.Resolve(ctx, v.ID, v.Proof, v.Attempts)
			}
		default:
			respond(w, 404, map[string]string{"error": "not_found"})
			return
		}
		if e != nil {
			respond(w, 409, map[string]string{"error": "billing_operation_rejected"})
			return
		}
		respond(w, 200, map[string]string{"status": "applied"})
	})
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
