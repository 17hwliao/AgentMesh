package embedding

import (
	"context"
	"errors"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/billing"
	"agentmesh/internal/provider"
	"agentmesh/internal/reservation"
)

type MeteredProvider interface {
	Provider
	BillingModel(string) string
	EmbedMetered(context.Context, string, []string, reservation.StreamSession) ([][]float64, *provider.Usage, error)
}
type receiptKey struct{}
type moneyReceipt struct {
	ID    string
	Usage *provider.Usage
}
type billedProvider struct {
	inner MeteredProvider
	gate  reservation.StreamGate
}

// EnableBilling also wraps cache embedding calls, so cloud cache lookups and
// failed cache hits cannot bypass the tenant's monetary wallet.
func EnableBilling(providers []Provider, gate reservation.StreamGate) ([]Provider, []billing.ModelBinding, error) {
	if gate == nil || len(providers) != 1 {
		return nil, nil, ErrUnavailable
	}
	p, ok := providers[0].(MeteredProvider)
	if !ok || p.BillingModel("embedding") == "" {
		return nil, nil, ErrUnavailable
	}
	return []Provider{&billedProvider{p, gate}}, []billing.ModelBinding{{Provider: p.Name(), Model: "embedding", UpstreamModel: p.BillingModel("embedding")}}, nil
}
func (p *billedProvider) Name() string                    { return p.inner.Name() }
func (p *billedProvider) BillableModel(alias string) bool { return alias == "embedding" }
func (p *billedProvider) Embed(ctx context.Context, _ string, inputs []string) (vectors [][]float64, err error) {
	tenant, ok := auth.TenantID(ctx)
	if !ok {
		return nil, ErrUnavailable
	}
	messages := make([]provider.Message, len(inputs))
	for i, text := range inputs {
		messages[i] = provider.Message{Role: "user", Content: text}
	}
	session, err := p.gate.Begin(ctx, tenant, "embedding", messages)
	if err != nil {
		return nil, err
	}
	receipt, _ := ctx.Value(receiptKey{}).(*moneyReceipt)
	if id, ok := session.(interface{ BillingID() string }); ok && receipt != nil {
		receipt.ID = id.BillingID()
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if finalErr := session.Complete(cleanup, "embedding_completed", ctx.Err() != nil); finalErr != nil && err == nil {
			vectors = nil
			err = finalErr
		}
	}()
	vectors, usage, err := p.inner.EmbedMetered(ctx, "embedding", inputs, session)
	if receipt != nil {
		receipt.Usage = usage
	}
	return vectors, err
}
func moneyError(err error) (int, string) {
	if errors.Is(err, billing.ErrInsufficient) {
		return 402, "billing_insufficient_balance"
	}
	if errors.Is(err, billing.ErrInvalid) || errors.Is(err, billing.ErrConflict) {
		return 503, "billing_unavailable"
	}
	return 0, ""
}
