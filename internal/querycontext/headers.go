package querycontext

import (
	"context"
	"maps"
)

type headersKey struct{}

func WithHeaders(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, headersKey{}, maps.Clone(headers))
}
func Headers(ctx context.Context) map[string]string {
	value, _ := ctx.Value(headersKey{}).(map[string]string)
	return maps.Clone(value)
}
