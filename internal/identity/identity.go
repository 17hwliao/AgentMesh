package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Kind identifies how a principal authenticated.
type Kind string

const (
	KindAdmin  Kind = "admin"
	KindLocal  Kind = "local"
	KindAppKey Kind = "app_key"
)

var (
	ErrEmptyID     = errors.New("identity: principal ID is empty")
	ErrUnknownKind = errors.New("identity: unknown principal kind")
)

// Principal is the authenticated identity attached to a request context.
type Principal struct {
	ID       string
	TenantID string
	Name     string
	Kind     Kind
	Scopes   []string
}

// Validate checks that the principal has an ID and a supported kind.
func (p Principal) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return ErrEmptyID
	}
	switch p.Kind {
	case KindAdmin, KindLocal, KindAppKey:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownKind, p.Kind)
	}
}

// HasScope reports whether the principal has the exact scope.
func (p Principal) HasScope(scope string) bool {
	for _, candidate := range p.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

type contextKey struct{}

// WithPrincipal returns a copy of ctx carrying the principal.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, principal)
}

// FromContext returns the principal carried by ctx, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	principal, ok := ctx.Value(contextKey{}).(Principal)
	return principal, ok
}
