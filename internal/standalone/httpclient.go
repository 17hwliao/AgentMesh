package standalone

import (
	"crypto/tls"
	"net/http"
	"time"

	"agentmesh/internal/catalog"
	"agentmesh/internal/telemetry"
)

func providerHTTPClient(provider catalog.Provider, timeout time.Duration) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	transport := base.Clone()
	if provider.TLSInsecure {
		config := &tls.Config{}
		if transport.TLSClientConfig != nil {
			config = transport.TLSClientConfig.Clone()
		}
		// #nosec G402 -- this is an explicit local setting for self-signed
		// AgentMesh endpoints; it is never implied by the URL scheme.
		config.InsecureSkipVerify = true
		transport.TLSClientConfig = config
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: telemetry.Transport{Base: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
