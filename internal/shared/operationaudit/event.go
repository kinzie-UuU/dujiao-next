// Package operationaudit carries explicitly allowlisted administrative changes.
package operationaudit

type Fields map[string]any

type Event struct {
	Action string
	Target string
	Result string
	Before Fields
	After  Fields
}

func New(action, target string, before Fields) *Event {
	return &Event{Action: action, Target: target, Result: "failed", Before: before}
}
func (e *Event) Success(after Fields) { e.Result = "success"; e.After = after }
