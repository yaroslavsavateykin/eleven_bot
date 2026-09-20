package schedule

import "context"

// MutationContext identifies the transport that requested a domain mutation.
// It keeps HTTP/Hermes writes out of the Telegram provenance and is also used
// by durable invocation receipts for retry-safe external calls.
type MutationContext struct {
	SourceType string
	ExternalID string
	SourceRef  string
	Announce   bool
}

type mutationContextKey struct{}

func WithMutationContext(ctx context.Context, value MutationContext) context.Context {
	return context.WithValue(ctx, mutationContextKey{}, value)
}

func mutationContext(ctx context.Context) MutationContext {
	value, _ := ctx.Value(mutationContextKey{}).(MutationContext)
	return value
}
