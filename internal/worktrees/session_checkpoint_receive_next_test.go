package worktrees

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
)

func TestSessionCheckpointReceiveNativeHarnessFallback(t *testing.T) {
	t.Parallel()
	var empty *sessionCheckpointPreflight
	empty.close()
	(&sessionCheckpointPreflight{}).close()
	for _, tc := range []struct{ native, agent, want string }{
		{" native ", " agent ", "native"}, {" \t", " agent ", "agent"}, {"", "", ""},
	} {
		if got := sourceNativeHarnessID(session.Record{NativeHarnessID: tc.native, AgentID: tc.agent}); got != tc.want {
			t.Fatalf("harness = %q, want %q", got, tc.want)
		}
	}
}

func TestSessionCheckpointReceiveRemoteOutputAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw, want string
		valid     bool
	}{
		{raw: "", valid: false}, {raw: "unterminated", valid: false}, {raw: "\n", valid: false},
		{raw: "first\nsecond\n", valid: false}, {raw: "value\r\n", valid: false},
		{raw: "https://github.com/acme/app.git\n", want: "https://github.com/acme/app.git", valid: true},
	} {
		got, err := parseCanonicalOriginRemoteOutput([]byte(tc.raw))
		if tc.valid {
			if err != nil || got != tc.want {
				t.Fatalf("valid output %q = %q %v", tc.raw, got, err)
			}
		} else if err == nil || got != "" {
			t.Fatalf("unsafe output %q admitted: %q %v", tc.raw, got, err)
		}
	}
}

func TestSessionCheckpointReceiveAuthorityRefusalPrecedesRoot(t *testing.T) {
	t.Parallel()
	state := &sessionReceiveState{}
	err := state.prepareSource(context.Background(), SessionMemberReceiveOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "validate target session receive authority") || state.canonical != nil || state.projectsRoot != "" {
		t.Fatalf("invalid authority admission = %v, state=%#v", err, state)
	}
}
