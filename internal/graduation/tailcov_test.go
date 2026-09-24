package graduation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestTailCovDecodeProducerEnvelopesRoundTripsEveryDecoder exercises the four
// decoder entry points that had no caller at all, and asserts each one returns
// the closed producer envelope it was given rather than merely not erroring.
func TestTailCovDecodeProducerEnvelopesRoundTripsEveryDecoder(t *testing.T) {
	t.Parallel()
	inputs, _ := validInputs()

	ciRaw, err := json.Marshal(inputs.CIWait)
	if err != nil {
		t.Fatal(err)
	}
	ci, err := DecodeCIWaitReceipt(ciRaw)
	if err != nil {
		t.Fatalf("DecodeCIWaitReceipt: %v", err)
	}
	if ci.Status != orchestrate.PullRequestWaitPassed || ci.Head != inputs.CIWait.Head || ci.Target != inputs.CIWait.Target || len(ci.Checks) != 1 {
		t.Fatalf("CI wait round trip = %#v", ci)
	}

	remoteRaw, err := json.Marshal(inputs.RemoteTarget)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := DecodeRemoteTarget(remoteRaw)
	if err != nil {
		t.Fatalf("DecodeRemoteTarget: %v", err)
	}
	if remote.Producer != RemoteTargetProducer || remote.TargetRef != "refs/heads/main" || remote.ObservedOutputSHA256 != inputs.RemoteTarget.ObservedOutputSHA256 {
		t.Fatalf("remote-target round trip = %#v", remote)
	}

	deployedRaw, err := json.Marshal(inputs.DeployedRevision)
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := DecodeDeployedRevision(deployedRaw)
	if err != nil {
		t.Fatalf("DecodeDeployedRevision: %v", err)
	}
	if deployed.Producer != DeploymentProducer || deployed.RevisionJSONPointer != "/deployment/revision" || deployed.PayloadSHA256 != inputs.DeployedRevision.PayloadSHA256 {
		t.Fatalf("deployed-revision round trip = %#v", deployed)
	}

	cleanupRaw, err := json.Marshal(inputs.TerminalCleanup)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := DecodeTerminalCleanup(cleanupRaw)
	if err != nil {
		t.Fatalf("DecodeTerminalCleanup: %v", err)
	}
	if !cleanup.Apply || !cleanup.DeleteRemote || cleanup.Phase != "applied" || len(cleanup.Results) != 1 || cleanup.Results[0].Repository != "sneat-dev/wb" {
		t.Fatalf("terminal-cleanup round trip = %#v", cleanup)
	}
}

// TestTailCovDecodeRejectsTrailingJSONValues pins the shared decoder contract
// that exactly one JSON document is accepted.
func TestTailCovDecodeRejectsTrailingJSONValues(t *testing.T) {
	t.Parallel()
	var value RemoteTargetEvidence

	if err := decode([]byte(`{} {}`), &value); err == nil || !strings.Contains(err.Error(), "unexpected trailing JSON value") {
		t.Fatalf("second complete JSON value was accepted: %v", err)
	}
	if err := decode([]byte(`{} trailing-garbage`), &value); err == nil || strings.Contains(err.Error(), "unexpected trailing JSON value") {
		t.Fatalf("malformed trailing bytes error = %v, want the decoder's own syntax error", err)
	}
	if err := decode([]byte(`{}`), &value); err != nil {
		t.Fatalf("single complete JSON value rejected: %v", err)
	}
}

// TestTailCovComposeRejectsMalformedEvidence closes every rejection branch the
// happy-path table never reached. Each case leaves the rest of the evidence
// valid so the asserted message identifies the exact rule that fired.
func TestTailCovComposeRejectsMalformedEvidence(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		change func(*Inputs, time.Time) time.Time
		want   string
	}{
		"missing receipt creation time": {
			change: func(_ *Inputs, _ time.Time) time.Time { return time.Time{} },
			want:   "creation time is required",
		},
		"local check is not schema v1": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.LocalCheck.SchemaVersion = SchemaVersion + 1
				return now
			},
			want: "one-repository schema v1",
		},
		"local check was not the ci profile": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.LocalCheck.Profile = "dev"
				return now
			},
			want: "one-repository schema v1",
		},
		"local check lacks the build mechanism": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				results := inputs.LocalCheck.Repositories[0].Results
				inputs.LocalCheck.Repositories[0].Results = results[:len(results)-1]
				return now
			},
			want: "lacks passed build mechanism",
		},
		"CI wait observed only skipping checks": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.CIWait.Checks[0].Bucket = "skipping"
				return now
			},
			want: "no passed CI mechanism",
		},
		"remote target observed payload conflicts": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.RemoteTarget.ObservedOutput = "deadbeef\trefs/heads/main\n"
				return now
			},
			want: "payload or digest conflicts",
		},
		"deployment payload is an empty object": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.PayloadJSON = "{}"
				inputs.DeployedRevision.PayloadSHA256 = Digest([]byte("{}"))
				return now
			},
			want: "non-empty structured JSON object",
		},
		"deployment payload is not an object": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.PayloadJSON = `["not","an","object"]`
				inputs.DeployedRevision.PayloadSHA256 = Digest([]byte(inputs.DeployedRevision.PayloadJSON))
				return now
			},
			want: "non-empty structured JSON object",
		},
		"cleanup report carries diagnostics": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.Diagnostics = []worktrees.ListDiagnostic{{Path: "/worktrees/graduation", Message: "unreadable"}}
				return now
			},
			want: "applied wb worktree cleanup --remote report",
		},
		"cleanup report repeats one repository": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.Results = append(inputs.TerminalCleanup.Results, inputs.TerminalCleanup.Results[0])
				return now
			},
			want: "invalid, duplicate, or cross-task repository result",
		},
		"cleanup report crosses tasks": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.Results[0].Task = "another-task"
				return now
			},
			want: "invalid, duplicate, or cross-task repository result",
		},
		"cleanup report lacks the graduated repository": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.TerminalCleanup.Results[0].Repository = "sneat-dev/other"
				return now
			},
			want: "lacks repository sneat-dev/wb",
		},
		"source digest has no sha256 prefix": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.RemoteTargetSHA256 = strings.Repeat("a", 64)
				return now
			},
			want: "source digest is not a sha256 digest",
		},
		"observation time is not the producer envelope time": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.RemoteTargetObservedAt = inputs.RemoteTarget.ObservedAt.Add(time.Second)
				return now
			},
			want: "closed producer envelopes",
		},
		"evidence timestamps are out of order": {
			change: func(inputs *Inputs, now time.Time) time.Time {
				inputs.DeployedRevision.ObservedAt = inputs.RemoteTarget.ObservedAt.Add(-time.Minute)
				inputs.DeployedObservedAt = inputs.DeployedRevision.ObservedAt
				return now
			},
			want: "must order local/CI, remote-target, deployed revision, then terminal cleanup",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inputs, now := validInputs()
			now = test.change(&inputs, now)
			if _, err := Compose(inputs, now); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Compose error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

// TestTailCovJSONPointerResolvesEscapesAndRejectsNonObjects pins RFC 6901
// navigation: escape sequences are decoded, a pointer may not cross a scalar,
// and a resolved value must still be a Git revision string.
func TestTailCovJSONPointerResolvesEscapesAndRejectsNonObjects(t *testing.T) {
	t.Parallel()
	// gitRevision only accepts lowercase hex, so the resolved leaf must be a
	// real lowercase revision for the escape path to return a value.
	revision := strings.Repeat("a", 40)

	tests := map[string]struct {
		root    any
		pointer string
		want    string
		wantErr string
	}{
		"root pointer is refused": {
			root:    map[string]any{"deployment": map[string]any{"revision": revision}},
			pointer: "deployment/revision",
			wantErr: "non-root RFC 6901 pointer",
		},
		"empty pointer is refused": {
			root:    map[string]any{"deployment": map[string]any{"revision": revision}},
			pointer: "",
			wantErr: "non-root RFC 6901 pointer",
		},
		"tilde escape resolves a key containing a tilde": {
			root:    map[string]any{"deploy~ment": map[string]any{"revision": revision}},
			pointer: "/deploy~0ment/revision",
			want:    strings.ToLower(revision),
		},
		"pointer crossing a scalar is refused": {
			root:    map[string]any{"deployment": "a-string"},
			pointer: "/deployment/revision",
			wantErr: "crosses a non-object",
		},
		"non-revision leaf is refused": {
			root:    map[string]any{"deployment": map[string]any{"revision": "not-a-revision"}},
			pointer: "/deployment/revision",
			wantErr: "does not resolve to a Git revision string",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := jsonPointerString(test.root, test.pointer)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("jsonPointerString(%q) error = %v, want one containing %q", test.pointer, err, test.wantErr)
				}
				if got != "" {
					t.Fatalf("jsonPointerString(%q) value = %q, want empty on error", test.pointer, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("jsonPointerString(%q): %v", test.pointer, err)
			}
			if got != test.want {
				t.Fatalf("jsonPointerString(%q) = %q, want %q", test.pointer, got, test.want)
			}
		})
	}
}

// TestTailCovComposeAcceptsEscapedRevisionPointer proves the escape handling is
// reachable through the public composer, not only the helper.
func TestTailCovComposeAcceptsEscapedRevisionPointer(t *testing.T) {
	t.Parallel()
	inputs, now := validInputs()
	payload := `{"deploy~ment":{"revision":"` + inputs.DeployedRevision.Revision + `"}}`
	inputs.DeployedRevision.PayloadJSON = payload
	inputs.DeployedRevision.PayloadSHA256 = Digest([]byte(payload))
	inputs.DeployedRevision.RevisionJSONPointer = "/deploy~0ment/revision"
	if _, err := Compose(inputs, now); err != nil {
		t.Fatalf("Compose rejected a valid escaped revision pointer: %v", err)
	}
}

// TestTailCovValidDigestAcceptsOnlyCanonicalSha256 pins the digest shape check
// independently of Compose's error wrapping.
func TestTailCovValidDigestAcceptsOnlyCanonicalSha256(t *testing.T) {
	t.Parallel()
	canonical := Digest([]byte("payload"))
	if !validDigest(canonical) {
		t.Fatalf("validDigest(%q) = false, want true", canonical)
	}
	for _, value := range []string{
		"",
		strings.TrimPrefix(canonical, "sha256:"),
		canonical + "00",
		"sha256:" + strings.ToUpper(strings.TrimPrefix(canonical, "sha256:")),
		"sha256:not-hex",
	} {
		if validDigest(value) {
			t.Errorf("validDigest(%q) = true, want false", value)
		}
	}
}

// TestTailCovComposeRequiresPassedLocalMechanisms pins the per-mechanism loop
// so each required mechanism is checked individually.
func TestTailCovComposeRequiresPassedLocalMechanisms(t *testing.T) {
	t.Parallel()
	for _, missing := range []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild} {
		inputs, now := validInputs()
		results := inputs.LocalCheck.Repositories[0].Results[:0]
		for _, entry := range inputs.LocalCheck.Repositories[0].Results {
			if entry.Check != missing {
				results = append(results, entry)
			}
		}
		inputs.LocalCheck.Repositories[0].Results = results
		_, err := Compose(inputs, now)
		if err == nil || !strings.Contains(err.Error(), "lacks passed "+string(missing)+" mechanism") {
			t.Fatalf("missing %s mechanism error = %v", missing, err)
		}
	}
}
