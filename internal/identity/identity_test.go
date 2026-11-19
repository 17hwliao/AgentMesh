package identity

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestWithPrincipalRoundTrip(t *testing.T) {
	want := Principal{
		ID:       "user-1",
		TenantID: "tenant-1",
		Name:     "Ada",
		Kind:     KindLocal,
		Scopes:   []string{"models:read", "models:write"},
	}

	got, ok := FromContext(WithPrincipal(context.Background(), want))
	if !ok {
		t.Fatal("principal not found in context")
	}
	if got.ID != want.ID || got.TenantID != want.TenantID || got.Name != want.Name || got.Kind != want.Kind {
		t.Fatalf("principal mismatch: got %+v, want %+v", got, want)
	}
	if !slices.Equal(got.Scopes, want.Scopes) {
		t.Fatalf("scopes mismatch: got %v, want %v", got.Scopes, want.Scopes)
	}
}

func TestFromContextMissingOrWrongType(t *testing.T) {
	if principal, ok := FromContext(context.Background()); ok || !emptyPrincipal(principal) {
		t.Fatalf("empty context returned principal %+v, %v", principal, ok)
	}

	ctx := context.WithValue(context.Background(), contextKey{}, "not a principal")
	if principal, ok := FromContext(ctx); ok || !emptyPrincipal(principal) {
		t.Fatalf("wrong context value returned principal %+v, %v", principal, ok)
	}
}

func emptyPrincipal(principal Principal) bool {
	return principal.ID == "" &&
		principal.TenantID == "" &&
		principal.Name == "" &&
		principal.Kind == "" &&
		principal.Scopes == nil
}

func TestHasScope(t *testing.T) {
	principal := Principal{Scopes: []string{"models:read", "usage:write"}}
	tests := []struct {
		name  string
		scope string
		want  bool
	}{
		{name: "present", scope: "models:read", want: true},
		{name: "case sensitive", scope: "Models:Read", want: false},
		{name: "missing", scope: "admin", want: false},
		{name: "empty", scope: "", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := principal.HasScope(test.scope); got != test.want {
				t.Fatalf("HasScope(%q) = %v, want %v", test.scope, got, test.want)
			}
		})
	}

	if (Principal{}).HasScope("models:read") {
		t.Fatal("principal without scopes unexpectedly has a scope")
	}
}

func TestPrincipalValidate(t *testing.T) {
	for _, kind := range []Kind{KindAdmin, KindLocal, KindAppKey} {
		t.Run(string(kind), func(t *testing.T) {
			principal := Principal{ID: "principal-1", Kind: kind}
			if err := principal.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	tests := []struct {
		name      string
		principal Principal
		wantErr   error
	}{
		{
			name:      "empty ID",
			principal: Principal{Kind: KindLocal},
			wantErr:   ErrEmptyID,
		},
		{
			name:      "whitespace ID",
			principal: Principal{ID: " \t ", Kind: KindLocal},
			wantErr:   ErrEmptyID,
		},
		{
			name:      "unknown kind",
			principal: Principal{ID: "principal-1", Kind: Kind("unknown")},
			wantErr:   ErrUnknownKind,
		},
		{
			name:      "empty kind",
			principal: Principal{ID: "principal-1"},
			wantErr:   ErrUnknownKind,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.principal.Validate(); !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
