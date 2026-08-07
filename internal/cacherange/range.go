package cacherange

import "context"

type contextKey struct{}

// Spec is a validated, inclusive single byte range.
//
// Absolute ranges (bytes=0-99) populate Start and End. Suffix ranges
// (bytes=-500 — the final 500 bytes of the representation) populate
// SuffixLength and leave Start/End zero; the range is resolved against the
// representation length when the cached body is served.
type Spec struct {
	Start        uint64
	End          uint64
	SuffixLength uint64
}

func WithContext(ctx context.Context, spec Spec) context.Context {
	return context.WithValue(ctx, contextKey{}, spec)
}

func FromContext(ctx context.Context) (Spec, bool) {
	spec, ok := ctx.Value(contextKey{}).(Spec)
	return spec, ok
}
