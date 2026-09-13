package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type invocationKey struct{}

// WithInvocation scopes mutation replay to one durable originating request.
func WithInvocation(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, invocationKey{}, key)
}

func ItemInvocation(ctx context.Context, index int) context.Context {
	if key := invocation(ctx); key != "" {
		return WithInvocation(ctx, fmt.Sprintf("%s/item/%d", key, index))
	}
	return ctx
}
func invocation(ctx context.Context) string {
	key, _ := ctx.Value(invocationKey{}).(string)
	return key
}

func (s Service) InvocationResult(ctx context.Context) ([]Event, bool, error) {
	key := invocation(ctx)
	if key == "" {
		return nil, false, nil
	}
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT result_json FROM agent_mutations WHERE group_id=? AND invocation_key=?", s.GroupID, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var events []Event
	err = json.Unmarshal([]byte(raw), &events)
	return events, err == nil, err
}
