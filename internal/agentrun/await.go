package agentrun

import (
	"context"
	"github.com/sneat-dev/wb/internal/agents"
	"time"
)

// Awaiter observes durable records; a terminal observation wins over cancellation.
type Awaiter struct {
	Load   func(string) (agents.Record, error)
	Render func(agents.Record) agents.Result
	Now    func() time.Time
	Wait   func(context.Context, time.Duration) error
}

func (a Awaiter) Await(ctx context.Context, id string, deadline time.Time) (agents.Result, error) {
	for {
		record, err := a.Load(id)
		if err != nil {
			return agents.Result{}, err
		}
		result := a.Render(record)
		if result.Terminal {
			return result, nil
		}
		if !deadline.IsZero() && !a.Now().Before(deadline) {
			return result, nil
		}
		if err := a.Wait(ctx, 250*time.Millisecond); err != nil {
			return result, err
		}
	}
}
