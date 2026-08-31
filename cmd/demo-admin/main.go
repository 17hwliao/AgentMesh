// Command demo-admin runs a short-lived, in-memory admin lifecycle demo.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"agentmesh/internal/admin"
	"agentmesh/internal/admindemo"
	"agentmesh/internal/auth"
	"agentmesh/internal/gateway"
	"agentmesh/internal/provider"
	"agentmesh/internal/tenant"
)

const adminToken = "demo-admin-token"

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "demo_admin_failed")
		os.Exit(1)
	}
}

func run(output io.Writer) error {
	store := admindemo.NewStore()
	mock := provider.NewMock(provider.MockConfig{Name: "demo-admin-mock", Chunks: []string{"admin demo response"}, FailAfterChunks: -1})
	resolver, err := tenant.NewResolver(store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		return err
	}
	server := gateway.NewWithTenantRouting(resolver)
	root := http.NewServeMux()
	root.Handle("/admin/", admin.NewHandler(store, sha256.Sum256([]byte(adminToken)), func(route []string) bool {
		return tenant.RouteAllowed(route, []string{"mock"})
	}))
	root.Handle("/", server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) }))
	httpServer := httptest.NewServer(root)
	defer httpServer.Close()
	client := httpServer.Client()

	if err := expectStatus(client, http.MethodPost, httpServer.URL+"/admin/tenants", adminToken, `{"tenant_id":"demo-tenant","model_routes":{"mock-model":["mock"]}}`, http.StatusCreated, nil); err != nil {
		return err
	}
	key, err := createKey(client, httpServer.URL, "demo-tenant")
	if err != nil {
		return err
	}
	if err := chat(client, httpServer.URL, key.Raw, http.StatusOK, ""); err != nil {
		return err
	}
	if err := expectStatus(client, http.MethodDelete, httpServer.URL+"/admin/api-keys/"+key.ID, adminToken, "", http.StatusNoContent, nil); err != nil {
		return err
	}
	if err := chat(client, httpServer.URL, key.Raw, http.StatusUnauthorized, auth.CodeFailed); err != nil {
		return err
	}
	replacement, err := createKey(client, httpServer.URL, "demo-tenant")
	if err != nil {
		return err
	}
	if err := chat(client, httpServer.URL, replacement.Raw, http.StatusOK, ""); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(output, "create_tenant_and_key=201")
	_, _ = fmt.Fprintln(output, "chat_with_new_key=200 provider=demo-admin-mock")
	_, _ = fmt.Fprintln(output, "revoke_key=204")
	_, _ = fmt.Fprintln(output, "chat_with_revoked_key=401 code=auth_failed")
	_, _ = fmt.Fprintln(output, "chat_with_replacement_key=200 provider=demo-admin-mock")
	return nil
}

type createdKey struct {
	ID  string `json:"key_id"`
	Raw string `json:"api_key"`
}

func createKey(client *http.Client, baseURL, tenantID string) (createdKey, error) {
	var result createdKey
	err := expectStatus(client, http.MethodPost, baseURL+"/admin/api-keys", adminToken, `{"tenant_id":"`+tenantID+`"}`, http.StatusCreated, &result)
	if err != nil || result.ID == "" || result.Raw == "" {
		return createdKey{}, fmt.Errorf("create key failed")
	}
	return result, nil
}

func chat(client *http.Client, baseURL, key string, want int, wantCode string) error {
	if wantCode == "" {
		return expectStatus(client, http.MethodPost, baseURL+"/v1/chat/completions", key, `{"model":"mock-model","messages":[{"role":"user","content":"admin demo"}],"stream":true}`, want, nil)
	}
	return expectStatus(client, http.MethodPost, baseURL+"/v1/chat/completions", key, `{"model":"mock-model","messages":[{"role":"user","content":"admin demo"}],"stream":true}`, want, nil, wantCode)
}

func expectStatus(client *http.Client, method, url, token, body string, want int, output any, code ...string) error {
	request, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != want {
		return fmt.Errorf("unexpected HTTP status")
	}
	if len(code) == 1 && !bytes.Contains(payload, []byte(`"code":"`+code[0]+`"`)) {
		return fmt.Errorf("unexpected HTTP error code")
	}
	if output != nil && json.Unmarshal(payload, output) != nil {
		return fmt.Errorf("unexpected HTTP response")
	}
	return nil
}
