package worktreeclaims

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

func testDirectory(t *testing.T) *os.File {
	t.Helper()
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}

func TestPublicationClaimStageReceipts(t *testing.T) {
	directory := testDirectory(t)
	claim := Claim{EffortID: "effort", RunID: "run", ClaimID: "claim", Worktree: "worktree", RecordedAt: time.Unix(100, 0).UTC()}
	stages := []string{"open-claims", "write-claim", "after-claim", "run-index", "projection", "journal", "after-projection", "open-outbox", "write-outbox", "success"}
	for _, failure := range stages {
		t.Run(failure, func(t *testing.T) {
			calls := []string{}
			step := func(name string) error {
				calls = append(calls, name)
				if name == failure {
					return errors.New(name)
				}
				return nil
			}
			ports := PublicationPorts{
				ReadClaimAt:         func(*os.File, string) (Claim, error) { return Claim{}, os.ErrNotExist },
				ReadClaimNames:      func(*os.File) ([]string, error) { return nil, nil },
				CorroborateExisting: func(Claim) error { return nil },
				OpenPrivateChild: func(_ *os.File, name string, _ bool) (*os.File, error) {
					if err := step("open-claims"); err != nil {
						return nil, err
					}
					return os.Open(directory.Name())
				},
				WriteJSONImmutableAt: func(_ *os.File, name string, value any, _ bool) error {
					if name == "claim.json" {
						return step("write-claim")
					}
					if _, ok := value.(ClaimPublicEvent); !ok {
						t.Fatalf("outbox value %T", value)
					}
					return step("write-outbox")
				},
				EnsureRunIndex: func(*os.File, string, string) error { return step("run-index") },
				WriteProjection: func(_ string, value Projection) error {
					if value.ClaimID != "claim" {
						t.Fatal(value)
					}
					return step("projection")
				},
				WriteCreationJournal: func(Claim) error { return step("journal") },
				OpenOutbox: func(string, string, bool) (*os.File, error) {
					if err := step("open-outbox"); err != nil {
						return nil, err
					}
					return os.Open(directory.Name())
				},
			}
			got, err := ports.PublishClaim("home", directory, "runpath", claim, PublicationHooks{
				AfterClaim: func() error { return step("after-claim") }, AfterProjection: func() error { return step("after-projection") },
			})
			if failure == "success" {
				if err != nil || !got.ClaimWritten || !got.ProjectionWritten || !got.OutboxWritten || got.ClaimPath != filepath.Join("runpath", "claims", "claim.json") {
					t.Fatalf("receipt=%+v err=%v", got, err)
				}
			} else if err == nil {
				t.Fatal("expected stage failure")
			}
			if failure == "open-claims" || failure == "write-claim" {
				if got.ClaimWritten {
					t.Fatal(got)
				}
			}
			if failure == "projection" || failure == "journal" {
				if got.OutboxWritten {
					t.Fatal(got)
				}
			}
			if len(calls) == 0 {
				t.Fatal("no effects")
			}
		})
	}
}

func TestActiveClaimReadPorts(t *testing.T) {
	directory := testDirectory(t)
	fail := ""
	calls := []string{}
	ports := ActiveClaimPorts{
		ReadProjectionForClaim: func(string, string) (Projection, error) {
			calls = append(calls, "mutating-read")
			if fail == "projection" {
				return Projection{}, errors.New(fail)
			}
			if fail == "lifecycle" {
				return Projection{Lifecycle: "terminal"}, nil
			}
			return Projection{Lifecycle: "active", EffortID: "e", RunID: "r", ClaimID: "c"}, nil
		},
		ReadProjectionReadOnly: func(string) (Projection, error) {
			calls = append(calls, "read-only")
			return Projection{Lifecycle: "active", EffortID: "e", RunID: "r", ClaimID: "c"}, nil
		},
		Corroborate: func(string, string, Projection) error {
			calls = append(calls, "corroborate")
			if fail == "corroborate" {
				return errors.New(fail)
			}
			return nil
		},
		OpenRun: func(string, string, string, bool) (*os.File, string, error) {
			calls = append(calls, "open-run")
			if fail == "open-run" {
				return nil, "", errors.New(fail)
			}
			dir, err := os.Open(directory.Name())
			return dir, "runpath", err
		},
		ReadClaimAt: func(*os.File, string) (Claim, error) {
			calls = append(calls, "read-claim")
			if fail == "read-claim" {
				return Claim{}, errors.New(fail)
			}
			return Claim{ClaimID: "c"}, nil
		},
	}
	for _, kind := range []string{"projection", "lifecycle", "corroborate", "open-run", "read-claim", "success"} {
		fail = kind
		_, _, _, err := ports.ActiveWorkLogClaim("home", "worktree")
		if kind == "success" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatalf("%s accepted", kind)
		}
	}
	fail = ""
	claim, _, path, err := ports.ActiveWorkLogClaimReadOnly("home", "worktree")
	if err != nil || claim.ClaimID != "c" || path != filepath.Join("runpath", "claims", "c.json") || calls[len(calls)-4] != "read-only" {
		t.Fatalf("read-only claim=%+v path=%q calls=%v err=%v", claim, path, calls, err)
	}
}

func TestLocalJournalOperationPorts(t *testing.T) {
	directory := testDirectory(t)
	failure := ""
	custody := 0
	ports := LocalJournalPorts{
		EnsureExclude: func(string) error {
			if failure == "exclude" {
				return errors.New(failure)
			}
			return nil
		},
		OpenDirectory: func(string, bool) (*os.File, error) {
			if failure == "open" {
				return nil, errors.New(failure)
			}
			return os.Open(directory.Name())
		},
		EnsureCustody: func(string) { custody++ },
		Lock: func(*os.File) (func(), error) {
			if failure == "lock" {
				return nil, errors.New(failure)
			}
			return func() {}, nil
		},
		AppendUnderLock: func(_ string, _ *os.File, event worktreejournal.LocalWorkLogEvent) (worktreejournal.LocalWorkLogEvent, worktreejournal.LocalWorkLogProjection, error) {
			return event, worktreejournal.LocalWorkLogProjection{}, nil
		},
		RebuildProjection: func([]worktreejournal.LocalWorkLogEvent) (worktreejournal.LocalWorkLogProjection, error) {
			if failure == "rebuild" {
				return worktreejournal.LocalWorkLogProjection{}, errors.New(failure)
			}
			return worktreejournal.LocalWorkLogProjection{}, nil
		},
		ReadManifestIdentity: func(string) (LocalJournalIdentity, error) {
			if failure == "manifest" {
				return LocalJournalIdentity{}, errors.New(failure)
			}
			return LocalJournalIdentity{EffortID: "manifest"}, nil
		},
		ReadHybridProjection: func(string) (LocalJournalIdentity, error) {
			if failure == "hybrid" {
				return LocalJournalIdentity{}, errors.New(failure)
			}
			return LocalJournalIdentity{EffortID: "hybrid", Lifecycle: "terminal"}, nil
		},
	}
	for _, kind := range []string{"exclude", "open", "lock"} {
		failure = kind
		_, _, err := ports.AppendLocalEvent("worktree", worktreejournal.LocalWorkLogEvent{Type: "test"})
		if err == nil {
			t.Fatalf("%s accepted", kind)
		}
	}
	failure = ""
	event, _, err := ports.AppendLocalEvent("worktree", worktreejournal.LocalWorkLogEvent{Type: "test", At: time.Now()})
	if err != nil || event.Version != 1 || custody != 4 {
		t.Fatalf("event=%+v custody=%d err=%v", event, custody, err)
	}
	_, _, err = ports.AppendLocalEvent("worktree", worktreejournal.LocalWorkLogEvent{Version: 2, Type: "test"})
	if err == nil {
		t.Fatal("version accepted")
	}
	_, _, err = ports.AppendLocalEvent("worktree", worktreejournal.LocalWorkLogEvent{})
	if err == nil {
		t.Fatal("type accepted")
	}
	before := custody
	_, _, err = ports.AppendLocalEventWithoutCustody("worktree", worktreejournal.LocalWorkLogEvent{Type: "test"})
	if err != nil || custody != before {
		t.Fatalf("custody=%d err=%v", custody, err)
	}
	_, _, err = ports.AppendLocalEvent("worktree", worktreejournal.LocalWorkLogEvent{Type: LocalEventOwner})
	if err != nil || custody != before {
		t.Fatalf("owner custody=%d err=%v", custody, err)
	}
	for _, kind := range []string{"rebuild", "manifest", "hybrid", "success"} {
		failure = kind
		projection, err := ports.ProjectLocalWorkLog("worktree", nil)
		if kind == "rebuild" {
			if err == nil {
				t.Fatal("rebuild accepted")
			}
		} else if err != nil {
			t.Fatal(err)
		} else if kind == "success" && (projection.EffortID != "hybrid" || projection.Lifecycle != "terminal") {
			t.Fatal(projection)
		}
	}
}

func TestGitAndUsageEvidencePorts(t *testing.T) {
	values := map[string]string{"branch": " feature ", "rev-parse": " head ", "status": " M file "}
	ports := LocalJournalPorts{Git: func(_ context.Context, _ string, args ...string) (string, error) {
		if value, ok := values[args[0]]; ok {
			return value, nil
		}
		return "", errors.New("git")
	}}
	got := ports.ObserveLocalGit(context.Background(), "worktree")
	if got.Branch != "feature" || got.Head != "head" || !got.Dirty || got.StatusSHA == "" {
		t.Fatal(got)
	}
	delete(values, "branch")
	delete(values, "rev-parse")
	delete(values, "status")
	if absent := ports.ObserveLocalGit(context.Background(), "worktree"); absent.Branch != "" || absent.Head != "" || absent.Status != "" {
		t.Fatal(absent)
	}
	gitPorts := GitEvidencePorts{Git: func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "rev-list":
			return "2 3", nil
		case "branch":
			return " feature ", nil
		case "rev-parse":
			return "sha", nil
		}
		return "", errors.New("git")
	}}
	if a, b, err := gitPorts.AheadBehind(context.Background(), "worktree", "sha"); err != nil || a != 2 || b != 3 {
		t.Fatalf("ahead=%d behind=%d err=%v", a, b, err)
	}
	if ok, err := gitPorts.BranchPublished(context.Background(), "worktree"); err != nil || !ok {
		t.Fatalf("published=%v err=%v", ok, err)
	}
	for _, raw := range []string{"", "2", "x 3"} {
		gitPorts.Git = func(context.Context, string, ...string) (string, error) { return raw, nil }
		if _, _, err := gitPorts.AheadBehind(context.Background(), "w", "sha"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	gitPorts.Git = func(context.Context, string, ...string) (string, error) { return "", errors.New("git") }
	if _, _, err := gitPorts.AheadBehind(context.Background(), "w", "sha"); err == nil {
		t.Fatal("git failure accepted")
	}
	if _, err := gitPorts.BranchPublished(context.Background(), "w"); err == nil {
		t.Fatal("branch failure accepted")
	}
	gitPorts.Git = func(context.Context, string, ...string) (string, error) { return " ", nil }
	if ok, _ := gitPorts.BranchPublished(context.Background(), "w"); ok {
		t.Fatal("empty branch published")
	}
	gitPorts.Git = func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "branch" {
			return "feature", nil
		}
		return "", errors.New("missing remote")
	}
	if ok, err := gitPorts.BranchPublished(context.Background(), "w"); ok || err != nil {
		t.Fatalf("remote=%v err=%v", ok, err)
	}
	if MustCountOutbox("w", func(string) (int, error) { return 3, errors.New("ignored") }) != 3 {
		t.Fatal("count")
	}
	input, output := int64(2), int64(3)
	cost := 1.5
	if usage, err := ObserveUsage("provider_reported", &input, &output, &cost, " usd ", " ref "); err != nil || usage.TotalTokens == nil || *usage.TotalTokens != 5 || usage.Currency != "usd" {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	if usage, err := ObserveUsage("", nil, nil, nil, "", ""); err != nil || usage != nil {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	for _, disc := range []string{"", "bogus"} {
		if _, err := ObserveUsage(disc, &input, nil, nil, "", ""); err == nil {
			t.Fatalf("accepted %q", disc)
		}
	}
	if usage, err := ObserveUsage("estimated", nil, &output, nil, "", ""); err != nil || usage.TotalTokens == nil || *usage.TotalTokens != 3 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	if usage, err := ObserveUsage("unavailable", nil, nil, nil, "", ""); err != nil || usage.TotalTokens != nil {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
	if !reflect.DeepEqual(*PtrLocalGit(got), got) {
		t.Fatal("pointer copy")
	}
}

func TestHistoricalInspectionPorts(t *testing.T) {
	directory := testDirectory(t)
	errStrict := errors.New("strict")
	event := worktreejournal.LocalWorkLogEvent{Type: worktreejournal.LocalEventHandoff, Result: "completed", Message: "parked successor proved live; target member custody completed", Extra: map[string]any{}}
	keys := []string{"resume_id", "parked_session_id", "member_id", "repository", "predecessor_wb_session_id", "successor_wb_session_id", "source_work_log_reference", "target_work_log_reference", "attempt_id"}
	for _, key := range keys {
		event.Extra[key] = "value"
	}
	if !HistoricalParkedCompletionShape(event) {
		t.Fatal("historical shape")
	}
	changed := event
	changed.Extra = map[string]any{}
	if HistoricalParkedCompletionShape(changed) {
		t.Fatal("empty extra")
	}
	for _, key := range keys {
		copyEvent := event
		copyEvent.Extra = map[string]any{}
		for k, v := range event.Extra {
			copyEvent.Extra[k] = v
		}
		delete(copyEvent.Extra, key)
		if HistoricalParkedCompletionShape(copyEvent) {
			t.Fatalf("missing %s", key)
		}
		copyEvent.Extra[key] = 1
		if HistoricalParkedCompletionShape(copyEvent) {
			t.Fatalf("nonstring %s", key)
		}
		copyEvent.Extra[key] = " "
		if HistoricalParkedCompletionShape(copyEvent) {
			t.Fatalf("blank %s", key)
		}
	}
	content := []byte("{\"version\":1,\"type\":\"init\"}\n")
	// The strict reader fails on the historical suffix. Only its valid prefix may be returned.
	last := `{"version":0,"type":"handoff","result":"completed","message":"parked successor proved live; target member custody completed","extra":{`
	for i, key := range keys {
		if i > 0 {
			last += ","
		}
		last += `"` + key + `":"value"`
	}
	last += `}}` + "\n"
	content = append(content, []byte(last)...)
	failure := ""
	ports := LocalJournalPorts{
		EnsureExclude: func(string) error { return nil }, OpenDirectory: func(string, bool) (*os.File, error) {
			if failure == "open" {
				return nil, errors.New("open")
			}
			return os.Open(directory.Name())
		},
		ReadEvents: func(string) ([]worktreejournal.LocalWorkLogEvent, error) {
			if failure == "strict-success" {
				return []worktreejournal.LocalWorkLogEvent{{Type: "init"}}, nil
			}
			return nil, errStrict
		},
		ReadBytesAt: func(*os.File, string) ([]byte, error) {
			if failure == "read" {
				return nil, errors.New("read")
			}
			if failure == "unterminated" {
				return content[:len(content)-1], nil
			}
			if failure == "malformed" {
				return []byte("bad\n"), nil
			}
			return content, nil
		},
		ParseEvents: func([]byte) ([]worktreejournal.LocalWorkLogEvent, error) {
			if failure == "prefix" {
				return nil, errors.New("prefix")
			}
			return []worktreejournal.LocalWorkLogEvent{{Type: "init"}}, nil
		},
	}
	for _, kind := range []string{"strict-success", "open", "read", "unterminated", "malformed", "prefix", "rejected", "success"} {
		failure = kind
		events, compat, err := ports.ReadLocalEventsForInspection("w", func(worktreejournal.LocalWorkLogEvent) bool { return failure != "rejected" })
		if kind == "strict-success" {
			if err != nil || compat || len(events) != 1 {
				t.Fatalf("%s events=%v compat=%v err=%v", kind, events, compat, err)
			}
		} else if kind == "success" {
			if err != nil || !compat || len(events) != 1 {
				t.Fatalf("%s events=%v compat=%v err=%v", kind, events, compat, err)
			}
		} else if !errors.Is(err, errStrict) {
			t.Fatalf("%s err=%v", kind, err)
		}
	}
}

func TestPublicationRetryUsesCorroboratedAuthority(t *testing.T) {
	directory := testDirectory(t)
	old := Claim{Version: 2, EffortID: "effort", RunID: "run", ClaimID: "claim", Worktree: "worktree", Model: "unknown", RecordedAt: time.Unix(100, 0).UTC(), WBSessionID: "original"}
	requested := old
	requested.RecordedAt = time.Unix(200, 0).UTC()
	requested.WBSessionID = "retry"
	if !SamePublicationRequest(old, requested, "") {
		t.Fatal("same request refused")
	}
	if SamePublicationRequest(old, requested, "retry") {
		t.Fatal("different caller-selected session accepted")
	}
	altered := old
	altered.Model = "different"
	if SamePublicationRequest(altered, requested, "") {
		t.Fatal("different model accepted")
	}
	altered = old
	altered.RecordedAt = time.Time{}
	if SamePublicationRequest(altered, requested, "") {
		t.Fatal("undated claim accepted")
	}
	existing := old
	readError := error(nil)
	corroborateError := error(nil)
	writeError := error(nil)
	readCount := 0
	claimWrites := 0
	eventAt := time.Time{}
	ports := PublicationPorts{
		ReadClaimNames:   func(*os.File) ([]string, error) { return nil, nil },
		OpenPrivateChild: func(*os.File, string, bool) (*os.File, error) { return os.Open(directory.Name()) },
		ReadClaimAt: func(*os.File, string) (Claim, error) {
			readCount++
			if readError != nil {
				return Claim{}, readError
			}
			return existing, nil
		},
		CorroborateExisting: func(Claim) error { return corroborateError },
		WriteJSONImmutableAt: func(_ *os.File, name string, value any, _ bool) error {
			if name == "claim.json" {
				claimWrites++
				return writeError
			}
			eventAt = value.(ClaimPublicEvent).At
			return nil
		},
		EnsureRunIndex: func(*os.File, string, string) error { return nil }, WriteProjection: func(string, Projection) error { return nil }, WriteCreationJournal: func(claim Claim) error {
			if !claim.RecordedAt.Equal(old.RecordedAt) {
				t.Fatalf("journal timestamp %s", claim.RecordedAt)
			}
			return nil
		},
		OpenOutbox: func(string, string, bool) (*os.File, error) { return os.Open(directory.Name()) },
	}
	receipt, err := ports.PublishClaim("home", directory, "runpath", requested, PublicationHooks{})
	if err != nil || claimWrites != 0 || !receipt.Claim.RecordedAt.Equal(old.RecordedAt) || !eventAt.Equal(old.RecordedAt) {
		t.Fatalf("receipt=%+v writes=%d eventAt=%s err=%v", receipt, claimWrites, eventAt, err)
	}
	existing.Model = "different"
	if _, err := ports.PublishClaim("home", directory, "runpath", requested, PublicationHooks{}); err == nil {
		t.Fatal("changed identity accepted")
	}
	existing = old
	corroborateError = errors.New("git mismatch")
	if _, err := ports.PublishClaim("home", directory, "runpath", requested, PublicationHooks{}); err == nil {
		t.Fatal("uncorroborated claim accepted")
	}
	corroborateError = nil
	readError = errors.New("corrupt record")
	if _, err := ports.PublishClaim("home", directory, "runpath", requested, PublicationHooks{}); err == nil {
		t.Fatal("unreadable claim accepted")
	}
	readError = os.ErrNotExist
	writeError = errors.New("no replace")
	if _, err := ports.PublishClaim("home", directory, "runpath", requested, PublicationHooks{}); err == nil {
		t.Fatal("write failure without authority accepted")
	}
	readError = nil
	readCount = 0
	ports.ReadClaimAt = func(*os.File, string) (Claim, error) {
		readCount++
		if readCount == 1 {
			return Claim{}, os.ErrNotExist
		}
		return existing, nil
	}
	receipt, err = ports.PublishClaim("home", directory, "runpath", requested, PublicationHooks{})
	if err != nil || !receipt.Claim.RecordedAt.Equal(old.RecordedAt) {
		t.Fatalf("concurrent first writer receipt=%+v err=%v", receipt, err)
	}
	existing.Model = "changed"
	readCount = 0
	if _, err := ports.PublishClaim("home", directory, "runpath", requested, PublicationHooks{}); err == nil {
		t.Fatal("conflicting concurrent first writer accepted")
	}
	existing = old
	readCount = 0
	corroborateError = errors.New("git mismatch")
	if _, err := ports.PublishClaim("home", directory, "runpath", requested, PublicationHooks{}); err == nil {
		t.Fatal("uncorroborated concurrent claim accepted")
	}
}

func TestPublicationRejectsConflictingRunClaims(t *testing.T) {
	directory := testDirectory(t)
	requested := Claim{ClaimID: strings.Repeat("b", 64), Worktree: "/checkout", RecordedAt: time.Unix(200, 0).UTC()}
	otherID := strings.Repeat("a", 64)
	names := []string{otherID + ".json"}
	listErr := error(nil)
	readErr := error(nil)
	other := Claim{ClaimID: otherID, Worktree: requested.Worktree}
	ports := PublicationPorts{
		OpenPrivateChild: func(*os.File, string, bool) (*os.File, error) { return os.Open(directory.Name()) },
		ReadClaimNames:   func(*os.File) ([]string, error) { return names, listErr },
		ReadClaimAt: func(_ *os.File, id string) (Claim, error) {
			if id == requested.ClaimID {
				return Claim{}, os.ErrNotExist
			}
			return other, readErr
		},
		WriteJSONImmutableAt: func(*os.File, string, any, bool) error { return nil },
		EnsureRunIndex:       func(*os.File, string, string) error { return nil },
		WriteProjection:      func(string, Projection) error { return nil },
		WriteCreationJournal: func(Claim) error { return nil },
		OpenOutbox:           func(string, string, bool) (*os.File, error) { return os.Open(directory.Name()) },
	}
	if _, err := ports.PublishClaim("home", directory, "run", requested, PublicationHooks{}); err == nil {
		t.Fatal("same worktree accepted under different claim ID")
	}
	listErr = errors.New("list failure")
	if _, err := ports.PublishClaim("home", directory, "run", requested, PublicationHooks{}); !errors.Is(err, listErr) {
		t.Fatalf("list err=%v", err)
	}
	listErr = nil
	names = []string{"." + otherID + ".json.tmp-scratch", "unsafe.json"}
	if _, err := ports.PublishClaim("home", directory, "run", requested, PublicationHooks{}); err == nil {
		t.Fatal("unsafe entry accepted")
	}
	names = []string{otherID + ".json"}
	readErr = errors.New("read failure")
	if _, err := ports.PublishClaim("home", directory, "run", requested, PublicationHooks{}); !errors.Is(err, readErr) {
		t.Fatalf("read err=%v", err)
	}
	readErr = nil
	other.ClaimID = "forged-identity"
	if _, err := ports.PublishClaim("home", directory, "run", requested, PublicationHooks{}); err == nil {
		t.Fatal("mismatched claim filename and identity accepted")
	}
	other.ClaimID = otherID
	other.Worktree = "/other-checkout"
	if _, err := ports.PublishClaim("home", directory, "run", requested, PublicationHooks{}); err != nil {
		t.Fatalf("other checkout blocked: %v", err)
	}
}
