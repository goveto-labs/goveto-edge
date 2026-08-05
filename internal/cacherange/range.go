package cacherange

import "context"

type contextKey struct{}

// Spec is a validated, inclusive single byte range.
type Spec struct {
	Start uint64
	End   uint64
}

func WithContext(ctx context.Context, spec Spec) context.Context {
	return context.WithValue(ctx, contextKey{}, spec)
}

func FromContext(ctx context.Context) (Spec, bool) {
	spec, ok := ctx.Value(contextKey{}).(Spec)
	return spec, ok
}
