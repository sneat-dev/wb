package orchestrate

import (
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func TestGitHubChecksPollIntervalDefaultsToQuotaAwareCadence(t *testing.T) {
	t.Parallel()
	if got := githubChecksPollInterval(Options{}); got != githubchecks.DefaultCheckPollInterval {
		t.Fatalf("default GitHub check poll interval = %s, want %s", got, githubchecks.DefaultCheckPollInterval)
	}
	if githubchecks.DefaultCheckPollInterval != 30*time.Second {
		t.Fatalf("quota-aware default = %s, want 30s", githubchecks.DefaultCheckPollInterval)
	}
}
