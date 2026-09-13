package agent

// ArgumentError marks a model-facing argument problem (bad JSON, wrong types).
// Its message is safe to expose to the model so it can correct its call.
type ArgumentError struct{ Message string }

func (e *ArgumentError) Error() string { return e.Message }

// ExecutionError reports a safe, model-facing domain failure. It never carries
// raw driver or SQLite text, so the agent can forward its message directly.
type ExecutionError struct{ Message string }

func (e *ExecutionError) Error() string { return e.Message }
