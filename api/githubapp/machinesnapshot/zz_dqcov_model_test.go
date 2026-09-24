package machinesnapshot

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// dqCovWorktree returns a worktree that passes Worktree.validate untouched, so
// each test can mutate exactly one field and attribute the failure.
func dqCovWorktree() Worktree {
	return Worktree{
		Task:       "dashboard",
		Repository: "acme/widgets",
		Branch:     "feature/dashboard",
	}
}

// dqCovRepositories builds n distinct canonical repository identities.
func dqCovRepositories(n int) []string {
	repositories := make([]string, 0, n)
	for index := 0; index < n; index++ {
		repositories = append(repositories, fmt.Sprintf("github.com/acme/repo-%05d", index))
	}
	return repositories
}

func TestDqCovResolveLatestRejectsCrossMachineComparison(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	current := StoredSnapshot{Snapshot: validSnapshot(at), Digest: "current"}
	candidate := current
	candidate.Snapshot.Machine = "desktop"
	candidate.Snapshot.PublishedAt = at.Add(time.Minute)
	candidate.Digest = "other"
	got, err := ResolveLatest(&current, candidate)
	if err == nil || !strings.Contains(err.Error(), "different machines") {
		t.Fatalf("ResolveLatest = (%+v, %v), want a cross-machine comparison error", got, err)
	}
	if got.Updated || got.Current.Digest != "" {
		t.Fatalf("ResolveLatest returned %+v for a rejected comparison", got)
	}
}

func TestDqCovSnapshotValidateRejectsMalformedEnvelopeFields(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*Snapshot){
		"schema version":        func(snapshot *Snapshot) { snapshot.SchemaVersion = SchemaVersion + 1 },
		"empty login":           func(snapshot *Snapshot) { snapshot.Login = "" },
		"dotted login":          func(snapshot *Snapshot) { snapshot.Login = ".alice" },
		"oversized login":       func(snapshot *Snapshot) { snapshot.Login = strings.Repeat("a", MaxIdentityLength+1) },
		"empty machine":         func(snapshot *Snapshot) { snapshot.Machine = "" },
		"machine with a space":  func(snapshot *Snapshot) { snapshot.Machine = "lap top" },
		"oversized machine":     func(snapshot *Snapshot) { snapshot.Machine = strings.Repeat("m", MaxIdentityLength+1) },
		"zero published_at":     func(snapshot *Snapshot) { snapshot.PublishedAt = time.Time{} },
		"too many repositories": func(snapshot *Snapshot) { snapshot.Repositories = dqCovRepositories(MaxRepositories + 1) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			snapshot := validSnapshot(at)
			mutate(&snapshot)
			err := snapshot.Validate()
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("Validate = %v, want ErrInvalidSnapshot", err)
			}
		})
	}
}

func TestDqCovWorktreeValidateRejectsEachMalformedField(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Worktree){
		"blank task":                     func(worktree *Worktree) { worktree.Task = "   " },
		"blank repository":               func(worktree *Worktree) { worktree.Repository = "\t" },
		"blank branch":                   func(worktree *Worktree) { worktree.Branch = " " },
		"control character in task":      func(worktree *Worktree) { worktree.Task = "dash\x01board" },
		"absolute task":                  func(worktree *Worktree) { worktree.Task = "/Users/alice/dashboard" },
		"oversized task":                 func(worktree *Worktree) { worktree.Task = strings.Repeat("t", MaxTaskLength+1) },
		"oversized stream":               func(worktree *Worktree) { worktree.Stream = strings.Repeat("s", MaxTaskLength+1) },
		"repository without an owner":    func(worktree *Worktree) { worktree.Repository = "widgets" },
		"repository with extra segments": func(worktree *Worktree) { worktree.Repository = "acme/widgets/extra" },
		"repository with a dotted owner": func(worktree *Worktree) { worktree.Repository = ".acme/widgets" },
		"repository with a space":        func(worktree *Worktree) { worktree.Repository = "acme/wide ts" },
		"oversized repository":           func(worktree *Worktree) { worktree.Repository = "acme/" + strings.Repeat("w", MaxRepositoryLen) },
		"oversized branch":               func(worktree *Worktree) { worktree.Branch = strings.Repeat("b", MaxBranchLength+1) },
		"oversized lifecycle":            func(worktree *Worktree) { worktree.Lifecycle = strings.Repeat("l", MaxStatusLength+1) },
		"oversized owner state":          func(worktree *Worktree) { worktree.OwnerState = strings.Repeat("s", MaxStatusLength+1) },
		"oversized owner":                func(worktree *Worktree) { worktree.Owner = strings.Repeat("o", MaxOwnerLength+1) },
		"oversized attention reason":     func(worktree *Worktree) { worktree.AttentionReason = strings.Repeat("a", MaxAttentionLen+1) },
		"control character in stream":    func(worktree *Worktree) { worktree.Stream = "str\x7feam" },
		"control character in owner":     func(worktree *Worktree) { worktree.Owner = "owner\nname" },
		"absolute stream path":           func(worktree *Worktree) { worktree.Stream = "/var/lib/wb" },
		"windows absolute owner path":    func(worktree *Worktree) { worktree.Owner = `C:\Users\alice` },
		"zero pull request number": func(worktree *Worktree) {
			worktree.PullRequest = &PullRequest{Number: 0, URL: "https://github.com/acme/widgets/pull/1"}
		},
		"oversized pull request URL": func(worktree *Worktree) {
			worktree.PullRequest = &PullRequest{Number: 1, URL: "https://github.com/" + strings.Repeat("u", MaxPRURLLength)}
		},
		"control character in pull request state": func(worktree *Worktree) {
			worktree.PullRequest = &PullRequest{Number: 1, URL: "https://github.com/acme/widgets/pull/1", State: "clo\x01sed"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			worktree := dqCovWorktree()
			mutate(&worktree)
			if err := worktree.validate(); err == nil {
				t.Fatal("validate accepted a malformed worktree")
			}
		})
	}
}

func TestDqCovWorktreeValidateAcceptsACompleteWorktree(t *testing.T) {
	t.Parallel()
	worktree := dqCovWorktree()
	worktree.TaskSummary = "one line summary"
	worktree.Stream = "stream-1"
	worktree.Lifecycle = "active"
	worktree.OwnerState = "running"
	worktree.Owner = "alice"
	worktree.AttentionReason = AttentionOwnerInactive
	worktree.PullRequest = &PullRequest{Number: 3, URL: "https://github.com/acme/widgets/pull/3", State: "open"}
	if err := worktree.validate(); err != nil {
		t.Fatalf("validate rejected a complete worktree: %v", err)
	}
}

func TestDqCovNormalizeAttentionReasonMapsToTheHostedAllowlist(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		needsAttention bool
		value          string
		want           string
	}{
		"no attention needed drops the reason": {false, AttentionOwnerInactive, ""},
		"no attention needed drops a bad one":  {false, "/Users/alice/private output", ""},
		"supported reason passes through":      {true, AttentionOwnerInactive, AttentionOwnerInactive},
		"supported review reason":              {true, AttentionSupersessionReview, AttentionSupersessionReview},
		"absorption reason passes through":     {true, AttentionAbsorptionReview, AttentionAbsorptionReview},
		"empty reason becomes the default":     {true, "", AttentionReviewRequired},
		"unknown reason becomes the default":   {true, "/Users/alice/private output", AttentionReviewRequired},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeAttentionReason(test.needsAttention, test.value); got != test.want {
				t.Fatalf("NormalizeAttentionReason(%v, %q) = %q, want %q", test.needsAttention, test.value, got, test.want)
			}
		})
	}
}

func TestDqCovLooksLikeAbsolutePathRecognizesHostPathForms(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{
		"/Users/alice/wb":   true,
		"/":                 true,
		`\\server\share`:    true,
		`C:\Users\alice`:    true,
		"c:/users/alice":    true,
		"relative/path":     false,
		"feature/dashboard": false,
		"C:":                false,
		"1:/weird":          false,
		"":                  false,
	} {
		if got := looksLikeAbsolutePath(value); got != want {
			t.Errorf("looksLikeAbsolutePath(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestDqCovPrintableRejectsControlCharactersAndInvalidUTF8(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		value string
		want  bool
	}{
		"plain text":         {"feature/dashboard", true},
		"tab is allowed":     {"column\tvalue", true},
		"empty":              {"", true},
		"newline is control": {"line\nbreak", false},
		"nul is control":     {"nul\x00byte", false},
		"delete is control":  {"del\x7fchar", false},
		"invalid utf8":       {"latin1-\xff-byte", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := printable(test.value); got != test.want {
				t.Fatalf("printable(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestDqCovValidateIdentityEnforcesTheHostedIdentifier(t *testing.T) {
	t.Parallel()
	if err := ValidateIdentity("alice-1.2_3"); err != nil {
		t.Fatalf("ValidateIdentity rejected a safe identity: %v", err)
	}
	for name, value := range map[string]string{
		"empty":       "",
		"too long":    strings.Repeat("a", MaxIdentityLength+1),
		"leading dot": ".alice",
		"path":        "../alice",
		"with space":  "ali ce",
		"with slash":  "ali/ce",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateIdentity(value); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("ValidateIdentity(%q) = %v, want ErrInvalidSnapshot", value, err)
			}
		})
	}
}

func TestDqCovSnapshotKeyRejectsAnInvalidMachine(t *testing.T) {
	t.Parallel()
	key, err := SnapshotKey("alice", "laptop-1")
	if err != nil || !strings.HasPrefix(key, "machine_") {
		t.Fatalf("SnapshotKey = (%q, %v), want a machine_ key", key, err)
	}
	if _, err := SnapshotKey("alice", "../laptop"); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("SnapshotKey with an invalid machine = %v, want ErrInvalidSnapshot", err)
	}
}

func TestDqCovSortPublishedOrdersBySnapshotKey(t *testing.T) {
	t.Parallel()
	snapshots := []PublishedSnapshot{
		{Snapshot: Snapshot{Login: "zoe", Machine: "laptop"}},
		{Snapshot: Snapshot{Login: "alice", Machine: "laptop"}},
		{Snapshot: Snapshot{Login: "alice", Machine: "desktop"}},
	}
	SortPublished(snapshots)
	got := make([]string, 0, len(snapshots))
	for _, published := range snapshots {
		got = append(got, published.Snapshot.Key())
	}
	want := []string{"alice/desktop", "alice/laptop", "zoe/laptop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortPublished left %v, want %v", got, want)
	}
}
