package fleet

import (
	"context"
	"time"

	"github.com/sneat-dev/wb/internal/herdr"
)

// activityTimeout bounds the herdr read of one refresh. herdr answers from a
// local socket, so a slower answer means it is stuck: the activity of that
// refresh is then not reported, and nothing else waits for it.
const activityTimeout = 3 * time.Second

// HerdrLister is the one herdr call the cockpit makes: the list of the agents
// herdr hosts. *herdr.Client satisfies it. The cockpit never reads a pane's
// screen text (herdr.Client.AgentRead): that is content, which belongs to
// cockpit-actions.
type HerdrLister interface {
	AgentList(ctx context.Context) ([]herdr.Agent, error)
}

// HerdrActivity is the ActivityCollector over herdr. Open returns the herdr
// client of a refresh, or an error when herdr is not installed; it is asked on
// every read, so installing herdr needs no restart.
type HerdrActivity struct {
	Open func() (HerdrLister, error)
}

// DefaultHerdrActivity reads the herdr found through HERDR_BIN_PATH or PATH,
// with the herdr call bounded by activityTimeout.
func DefaultHerdrActivity() HerdrActivity {
	return HerdrActivity{Open: func() (HerdrLister, error) {
		client, err := herdr.NewClient(herdr.OSLookupEnv, herdr.WithTimeout(activityTimeout))
		if err != nil {
			return nil, err
		}
		return client, nil
	}}
}

// Activity lists herdr's agents once and returns the status of each by its
// harness session id. An agent with no harness session id, or whose id two
// agents with different statuses share, is left out (the join is by id and is
// never guessed), and a status outside the five herdr documents reads as
// unknown.
func (h HerdrActivity) Activity(ctx context.Context) (map[string]string, error) {
	lister, err := h.Open()
	if err != nil {
		return nil, err
	}
	agents, err := lister.AgentList(ctx)
	if err != nil {
		return nil, err
	}
	activity := map[string]string{}
	ambiguous := map[string]bool{}
	for _, agent := range agents {
		id := agent.HarnessSessionID()
		if id == "" || ambiguous[id] {
			continue
		}
		status := activityOf(agent.Status)
		if known, seen := activity[id]; seen && known != status {
			delete(activity, id)
			ambiguous[id] = true
			continue
		}
		activity[id] = status
	}
	return activity, nil
}

// activityOf is the closed activity value of a herdr status.
func activityOf(status herdr.AgentStatus) string {
	switch status {
	case herdr.StatusWorking:
		return ActivityWorking
	case herdr.StatusBlocked:
		return ActivityBlocked
	case herdr.StatusIdle:
		return ActivityIdle
	case herdr.StatusDone:
		return ActivityDone
	}
	return ActivityUnknown
}
