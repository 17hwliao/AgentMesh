package modelrpc

import (
	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"bytes"
	"context"
	"encoding/json"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"math"
	"time"
)

type Server struct {
	UnimplementedModelRunnerServer
	Providers map[string]provider.Provider
	Authorize func(context.Context, string, string, string) bool
	slots     chan struct{}
}

func NewServer(providers []provider.Provider, authorize func(context.Context, string, string, string) bool) *Server {
	s := &Server{Providers: map[string]provider.Provider{}, Authorize: authorize, slots: make(chan struct{}, 32)}
	for _, p := range providers {
		s.Providers[p.Name()] = p
	}
	return s
}
func (s *Server) Health(ctx context.Context, in *HealthRequest) (*HealthResponse, error) {
	p, ok := s.Providers[in.Provider]
	if !ok {
		return nil, status.Error(codes.NotFound, "provider_not_found")
	}
	if in.ModelAlias != "" {
		if bound, ok := p.(interface{ BillingModel(string) string }); ok {
			return &HealthResponse{Healthy: true, UpstreamModel: bound.BillingModel(in.ModelAlias)}, nil
		}
		return nil, status.Error(codes.FailedPrecondition, "model_binding_unavailable")
	}
	return &HealthResponse{Healthy: p.Health(ctx) == nil}, nil
}
func (s *Server) Chat(in *Request, stream grpc.ServerStreamingServer[Chunk]) error {
	ctx, cancel := context.WithTimeout(stream.Context(), 2*time.Minute)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return status.Error(codes.ResourceExhausted, "runner_busy")
	}
	var request provider.ChatRequest
	d := json.NewDecoder(bytes.NewReader(in.ChatJson))
	d.DisallowUnknownFields()
	if len(in.ChatJson) > 64<<10 || len(in.TenantId) == 0 || len(in.TenantId) > 64 || d.Decode(&request) != nil || d.Decode(&struct{}{}) != io.EOF || len(request.Messages) == 0 || len(request.Messages) > 128 || request.MaxTokens < 1 || request.MaxTokens > 4096 || len(request.Model) > 128 {
		return status.Error(codes.InvalidArgument, "request_invalid")
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || *request.Temperature < 0 || *request.Temperature > 2) {
		return status.Error(codes.InvalidArgument, "temperature_invalid")
	}
	for _, m := range request.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			return status.Error(codes.InvalidArgument, "role_invalid")
		}
	}
	p, ok := s.Providers[in.Provider]
	if !ok || s.Authorize == nil || !s.Authorize(ctx, in.TenantId, request.Model, in.Provider) {
		return status.Error(codes.PermissionDenied, "route_not_allowed")
	}
	upstream := ""
	if bound, ok := p.(interface{ BillingModel(string) string }); ok {
		upstream = bound.BillingModel(request.Model)
	}
	if in.ExpectedUpstreamModel != "" && upstream != in.ExpectedUpstreamModel {
		return status.Error(codes.FailedPrecondition, "model_binding_changed")
	}
	total := 0
	e := p.Stream(ctx, request, func(c provider.Chunk) error {
		raw, e := json.Marshal(c)
		total += len(raw)
		if e != nil || len(raw) > 128<<10 || total > 1<<20 {
			return status.Error(codes.ResourceExhausted, "output_limit")
		}
		return stream.Send(&Chunk{ChunkJson: raw, UpstreamModel: upstream})
	})
	if e != nil {
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		return status.Error(codes.Unavailable, "provider_failed")
	}
	return stream.Send(&Chunk{Completed: true, UpstreamModel: upstream})
}

type Adapter struct {
	client       ModelRunnerClient
	ProviderName string
	Models       map[string]string
	DefaultModel string
}

func NewAdapter(conn grpc.ClientConnInterface, name string, models map[string]string, defaultModel string) *Adapter {
	return &Adapter{client: NewModelRunnerClient(conn), ProviderName: name, Models: models, DefaultModel: defaultModel}
}
func (a *Adapter) Name() string { return a.ProviderName }
func (a *Adapter) ValidateBilling(ctx context.Context, alias, expected string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, err := a.client.Health(ctx, &HealthRequest{Provider: a.ProviderName, ModelAlias: alias})
	if err != nil || r == nil || r.UpstreamModel != expected {
		return provider.ErrProtocol
	}
	return nil
}
func (a *Adapter) BillingModel(alias string) string {
	if len(a.Models) > 0 {
		return a.Models[alias]
	}
	return a.DefaultModel
}
func (a *Adapter) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, e := a.client.Health(ctx, &HealthRequest{Provider: a.ProviderName})
	if e != nil || r == nil || !r.Healthy {
		return provider.ErrUpstream
	}
	return nil
}
func (a *Adapter) Stream(ctx context.Context, r provider.ChatRequest, emit provider.Emit) error {
	tenant, ok := auth.TenantID(ctx)
	if !ok {
		return provider.ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if r.MaxTokens == 0 {
		r.MaxTokens = 1024
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return provider.ErrProtocol
	}
	expected := a.BillingModel(r.Model)
	stream, e := a.client.Chat(ctx, &Request{TenantId: tenant, Provider: a.ProviderName, ChatJson: raw, ExpectedUpstreamModel: expected})
	if e != nil {
		return provider.ErrUpstream
	}
	for {
		wire, e := stream.Recv()
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return provider.ErrProtocol
		}
		if expected != "" && wire.UpstreamModel != expected {
			return provider.ErrProtocol
		}
		if wire.Completed {
			return nil
		}
		var c provider.Chunk
		d := json.NewDecoder(bytes.NewReader(wire.ChunkJson))
		d.DisallowUnknownFields()
		if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF {
			return provider.ErrProtocol
		}
		if e = emit(c); e != nil {
			return e
		}
	}
}
