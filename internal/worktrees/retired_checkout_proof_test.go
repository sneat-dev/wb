package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreebranches"
	"github.com/sneat-dev/wb/internal/worktreeretire"
)

type retiredProofFixture struct {
	options      RetiredCheckoutProofOptions
	ports        retiredCheckoutProofPorts
	tx           worktreeretire.Transaction
	gitQuery     func(context.Context, string, ...string) (string, error)
	canonicalDir string
	readSnapshot func(string) (worktreeretire.ReportSnapshot, error)
}

func (fixture *retiredProofFixture) buildSnapshot() worktreeretire.ReportSnapshot {
	body, err := json.MarshalIndent(fixture.tx, "", "  ")
	if err != nil {
		panic(err)
	}
	body = append(body, '\n')
	digest := sha256.Sum256(body)
	return worktreeretire.ReportSnapshot{Transaction: fixture.tx, Bytes: body, SHA256: hex.EncodeToString(digest[:])}
}

func newRetiredProofFixture(t *testing.T) *retiredProofFixture {
	t.Helper()
	root := t.TempDir()
	canonicalDir := filepath.Join(root, "canonical")
	if err := os.MkdirAll(filepath.Join(canonicalDir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	candidate := strings.Repeat("a", 40)
	worktree := filepath.Join(root, "worktrees", "task", "app")
	canonical := canonicalDir
	home := filepath.Join(root, ".wb")
	path := worktreeretire.ReportPath(home, worktreeretire.Transaction{Task: "task", Repository: "acme/app"})
	tx := worktreeretire.Transaction{
		Version: 1, Task: "task", Repository: "acme/app", ArchiveRepository: "acme/backstage-retired",
		Worktree: worktree, Canonical: canonical, Branch: "feature", Preserve: "branch", SourceSHA: candidate,
		RetiredRef: "retired/20260101-feature-aaaaaaa", ArchiveRef: "retired/backstage-retired/20260101-feature-aaaaaaa",
		ArchiveSHA: strings.Repeat("e", 40), ClaimID: "claim", EffortID: "effort", RunID: "run", Phase: "complete", ReportPath: path,
	}
	fixture := &retiredProofFixture{
		options: RetiredCheckoutProofOptions{
			ProjectsRoot: root, Task: "task", Repository: "acme/app", CandidateWorktree: worktree,
			CandidateBranch: "feature", CandidateSHA: candidate, PublishedCandidateSHA: candidate, ArchiveRemote: "/remotes/archive.git",
		},
		tx:           tx,
		canonicalDir: canonicalDir,
	}
	fixture.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
		switch {
		case len(args) == 3 && args[0] == "worktree" && args[1] == "list":
			return "", nil
		case len(args) >= 3 && args[0] == "for-each-ref":
			return "", nil
		case len(args) == 4 && args[0] == "fetch":
			return "", nil
		case len(args) == 2 && args[0] == "rev-parse" && args[1] == "FETCH_HEAD":
			return candidate, nil
		case len(args) == 3 && args[0] == "cat-file" && args[1] == "-t":
			return "commit", nil
		default:
			return "", errors.New("unexpected git command: " + strings.Join(args, " "))
		}
	}
	fixture.readSnapshot = func(string) (worktreeretire.ReportSnapshot, error) {
		return fixture.buildSnapshot(), nil
	}
	fixture.ports = retiredCheckoutProofPorts{
		home:         func(string) (string, error) { return home, nil },
		canonical:    func(string, string) (string, error) { return canonical, nil },
		readSnapshot: func(path string) (worktreeretire.ReportSnapshot, error) { return fixture.readSnapshot(path) },
		openCanonical: func(string) (*canonicalRepository, error) {
			return openCanonicalRepository(fixture.canonicalDir)
		},
		archivePlan: func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error) {
			return RetiredArchivePlan{Outcome: "planned", ArchiveRepository: tx.ArchiveRepository}, nil
		},
		verifyRemoved: func(context.Context, worktreeretire.Operation, worktreeretire.Transaction) error { return nil },
		git: func(ctx context.Context, _ *canonicalRepository, args ...string) (string, error) {
			return fixture.gitQuery(ctx, canonical, args...)
		},
		lstat: func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
	}
	return fixture
}

func TestProveRetiredCheckoutAcceptsCompletedPreservedRetirement(t *testing.T) {
	t.Parallel()
	fixture := newRetiredProofFixture(t)
	evidence, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Retirement.SourceSHA != fixture.options.CandidateSHA || evidence.Canonical != fixture.tx.Canonical ||
		evidence.ReceiptPath != fixture.tx.ReportPath || len(evidence.ReceiptSHA256) != 64 || len(evidence.ProofSHA256) != 64 {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestProveRetiredCheckoutAcceptsCandidateOnlyAndAdvancedPublication(t *testing.T) {
	t.Parallel()
	for _, change := range []struct {
		name   string
		mutate func(*retiredProofFixture)
	}{
		{"candidate-only publication", func(f *retiredProofFixture) { f.options.PublishedCandidateSHA = "" }},
		{"published head advanced by preserved candidate", func(f *retiredProofFixture) {
			f.options.PublishedCandidateSHA = strings.Repeat("b", 40)
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRetiredProofFixture(t)
			change.mutate(fixture)
			if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err != nil {
				t.Fatalf("%s proof: %v", change.name, err)
			}
		})
	}
}

func TestProveRetiredCheckoutRefusesReceiptChangedDuringProof(t *testing.T) {
	t.Parallel()
	fixture := newRetiredProofFixture(t)
	original := fixture.readSnapshot
	calls := 0
	fixture.readSnapshot = func(path string) (worktreeretire.ReportSnapshot, error) {
		calls++
		if calls > 1 {
			changed := fixture.tx
			changed.Phase = "original_deleted"
			body, err := json.MarshalIndent(changed, "", "  ")
			if err != nil {
				return worktreeretire.ReportSnapshot{}, err
			}
			body = append(body, '\n')
			digest := sha256.Sum256(body)
			return worktreeretire.ReportSnapshot{Transaction: changed, Bytes: body, SHA256: hex.EncodeToString(digest[:])}, nil
		}
		return original(path)
	}
	if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err == nil || !strings.Contains(err.Error(), "changed while it was being proven") {
		t.Fatalf("receipt rewrite proof = %v", err)
	}
	if calls != 2 {
		t.Fatalf("receipt snapshot reads = %d", calls)
	}
}

func TestProveRetiredCheckoutRefusesIncompleteOrMismatchedEvidence(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected retirement proof refusal")
	cases := []struct {
		name   string
		change func(*retiredProofFixture)
		want   string
	}{
		{"missing candidate fields", func(f *retiredProofFixture) { f.options.Task = "" }, "requires the exact candidate"},
		{"relative candidate worktree", func(f *retiredProofFixture) { f.options.CandidateWorktree = "relative" }, "clean absolute path"},
		{"invalid candidate SHA", func(f *retiredProofFixture) { f.options.CandidateSHA = "not-an-object" }, "exact candidate commit object"},
		{"invalid published candidate SHA", func(f *retiredProofFixture) { f.options.PublishedCandidateSHA = "not-an-object" }, "exact published candidate commit object"},
		{"checkout still exists", func(f *retiredProofFixture) {
			f.ports.lstat = func(string) (os.FileInfo, error) {
				return fakeFileInfo{mode: os.ModeDir}, nil
			}
		}, "still exists"},
		{"checkout inspection failure", func(f *retiredProofFixture) {
			f.ports.lstat = func(string) (os.FileInfo, error) { return nil, boom }
		}, "inspect retired candidate checkout"},
		{"home resolution", func(f *retiredProofFixture) { f.ports.home = func(string) (string, error) { return "", boom } }, "injected retirement proof refusal"},
		{"canonical resolution", func(f *retiredProofFixture) {
			f.ports.canonical = func(string, string) (string, error) { return "", boom }
		}, "injected retirement proof refusal"},
		{"unreadable receipt", func(f *retiredProofFixture) {
			f.readSnapshot = func(string) (worktreeretire.ReportSnapshot, error) {
				return worktreeretire.ReportSnapshot{}, boom
			}
		}, "read retirement receipt"},
		{"wrong receipt path", func(f *retiredProofFixture) { f.tx.ReportPath = "/elsewhere.json" }, "authorize removed-checkout resume"},
		{"incomplete retirement", func(f *retiredProofFixture) { f.tx.Phase = "original_deleted" }, "want complete"},
		{"wrong candidate worktree", func(f *retiredProofFixture) { f.tx.Worktree = "/other" }, "does not bind the exact receipted candidate identity"},
		{"wrong canonical", func(f *retiredProofFixture) { f.tx.Canonical = "/other/canonical" }, "names canonical clone"},
		{"missing archive object", func(f *retiredProofFixture) { f.tx.ArchiveSHA = "" }, "no verified private archive object"},
		{"invalid archive object", func(f *retiredProofFixture) { f.tx.ArchiveSHA = "not-an-object" }, "no verified private archive object"},
		{"missing archive repository", func(f *retiredProofFixture) { f.tx.ArchiveRepository = "" }, "no usable preserved-source or private-archive identity"},
		{"non-retired source ref", func(f *retiredProofFixture) { f.tx.RetiredRef = "refs/heads/main" }, "no usable preserved-source or private-archive identity"},
		{"missing archive ref", func(f *retiredProofFixture) { f.tx.ArchiveRef = "" }, "no usable preserved-source or private-archive identity"},
		{"missing exact deletion intent", func(f *retiredProofFixture) {
			f.tx.OriginalRemoteSHA = strings.Repeat("f", 40)
		}, "no exact original-branch deletion intent"},
		{"canonical open", func(f *retiredProofFixture) {
			f.ports.openCanonical = func(string) (*canonicalRepository, error) { return nil, boom }
		}, "open canonical clone"},
		{"archive preflight failure", func(f *retiredProofFixture) {
			f.ports.archivePlan = func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error) {
				return RetiredArchivePlan{}, boom
			}
		}, "injected retirement proof refusal"},
		{"archive no longer private", func(f *retiredProofFixture) {
			f.ports.archivePlan = func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error) {
				return RetiredArchivePlan{Outcome: "refused", ArchiveRepository: f.tx.ArchiveRepository}, nil
			}
		}, "private archive preflight no longer holds"},
		{"unsafe archive remote", func(f *retiredProofFixture) { f.options.ArchiveRemote = "-option" }, "retirement archive remote"},
		{"preserved source proof", func(f *retiredProofFixture) {
			f.ports.verifyRemoved = func(context.Context, worktreeretire.Operation, worktreeretire.Transaction) error { return boom }
		}, "preserved-source proof"},
		{"worktree inventory", func(f *retiredProofFixture) {
			f.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "worktree" {
					return "", boom
				}
				return newRetiredProofFixtureDefaultGit(args)
			}
		}, "injected retirement proof refusal"},
		{"checkout still registered", func(f *retiredProofFixture) {
			f.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "worktree" {
					return "worktree " + f.options.CandidateWorktree + "\n", nil
				}
				return newRetiredProofFixtureDefaultGit(args)
			}
		}, "still registered"},
		{"branch enumeration", func(f *retiredProofFixture) {
			f.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "for-each-ref" {
					return "", boom
				}
				return newRetiredProofFixtureDefaultGit(args)
			}
		}, "injected retirement proof refusal"},
		{"local branch survives", func(f *retiredProofFixture) {
			f.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "for-each-ref" {
					return strings.Repeat("f", 40), nil
				}
				return newRetiredProofFixtureDefaultGit(args)
			}
		}, "retired local branch"},
		{"retired source fetch", func(f *retiredProofFixture) {
			f.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "fetch" {
					return "", boom
				}
				return newRetiredProofFixtureDefaultGit(args)
			}
		}, "fetch retired source ref"},
		{"fetched source mismatch", func(f *retiredProofFixture) {
			f.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "rev-parse" && args[1] == "FETCH_HEAD" {
					return strings.Repeat("b", 40), nil
				}
				return newRetiredProofFixtureDefaultGit(args)
			}
		}, "does not bind source SHA"},
		{"preserved source object missing", func(f *retiredProofFixture) {
			f.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "cat-file" && args[2] == f.tx.SourceSHA {
					return "", boom
				}
				return newRetiredProofFixtureDefaultGit(args)
			}
		}, "is missing"},
		{"candidate object missing", func(f *retiredProofFixture) {
			f.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] == "cat-file" && args[2] == f.options.CandidateSHA {
					return "", boom
				}
				return newRetiredProofFixtureDefaultGit(args)
			}
		}, "is missing"},
		{"receipt re-read failure", func(f *retiredProofFixture) {
			original := f.readSnapshot
			calls := 0
			f.readSnapshot = func(path string) (worktreeretire.ReportSnapshot, error) {
				calls++
				if calls > 1 {
					return worktreeretire.ReportSnapshot{}, boom
				}
				return original(path)
			}
		}, "re-read retirement receipt"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRetiredProofFixture(t)
			tt.change(fixture)
			if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("proof error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func newRetiredProofFixtureDefaultGit(args []string) (string, error) {
	switch {
	case len(args) == 3 && args[0] == "worktree":
		return "", nil
	case args[0] == "for-each-ref":
		return "", nil
	case args[0] == "fetch":
		return "", nil
	case len(args) == 2 && args[0] == "rev-parse" && args[1] == "FETCH_HEAD":
		return strings.Repeat("a", 40), nil
	case len(args) == 3 && args[0] == "cat-file":
		return "commit", nil
	default:
		return "", errors.New("unexpected git command: " + strings.Join(args, " "))
	}
}

type fakeFileInfo struct{ mode os.FileMode }

func (info fakeFileInfo) Name() string       { return "candidate" }
func (info fakeFileInfo) Size() int64        { return 0 }
func (info fakeFileInfo) Mode() os.FileMode  { return info.mode }
func (info fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (info fakeFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info fakeFileInfo) Sys() any           { return nil }

func TestProveRetiredSourceBindsCandidateThroughDurableIntent(t *testing.T) {
	t.Parallel()
	candidate := strings.Repeat("a", 40)
	source := strings.Repeat("b", 40)
	tree := strings.Repeat("c", 40)
	base := func() (*retiredProofFixture, worktreeretire.Transaction) {
		fixture := newRetiredProofFixture(t)
		tx := fixture.tx
		tx.SourceSHA = source
		tx.IntentParentSHA = candidate
		tx.IntentTreeSHA = tree
		tx.IntentMessage = "Retire worktree source changes"
		tx.IntentAt = time.Now().UTC()
		fixture.tx = tx
		fixture.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
			switch {
			case len(args) == 2 && args[0] == "rev-parse" && args[1] == "FETCH_HEAD":
				return source, nil
			case len(args) == 2 && args[0] == "rev-parse" && args[1] == source+"^":
				return candidate, nil
			case len(args) == 2 && args[0] == "rev-parse" && args[1] == source+"^{tree}":
				return tree, nil
			case len(args) == 4 && args[0] == "log":
				return tx.IntentMessage + "\n", nil
			default:
				return newRetiredProofFixtureDefaultGit(args)
			}
		}
		return fixture, tx
	}
	fixture, _ := base()
	if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err != nil {
		t.Fatalf("intent-bound proof: %v", err)
	}

	for _, change := range []struct {
		name   string
		mutate func(*worktreeretire.Transaction)
		want   string
	}{
		{"missing intent identity", func(tx *worktreeretire.Transaction) { tx.IntentParentSHA = "" }, "does not bind receipted candidate"},
		{"wrong intent tree", func(tx *worktreeretire.Transaction) { tx.IntentTreeSHA = "not-a-tree" }, "intent tree does not match"},
	} {
		t.Run(change.name, func(t *testing.T) {
			t.Parallel()
			fixture, tx := base()
			change.mutate(&tx)
			fixture.tx = tx
			if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err == nil || !strings.Contains(err.Error(), change.want) {
				t.Fatalf("intent proof error = %v, want containing %q", err, change.want)
			}
		})
	}

	t.Run("published candidate object missing", func(t *testing.T) {
		t.Parallel()
		fixture, _ := base()
		baseGit := fixture.gitQuery
		fixture.gitQuery = func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) == 3 && args[0] == "cat-file" && args[2] == candidate {
				return "", errors.New("candidate object missing")
			}
			return baseGit(ctx, dir, args...)
		}
		if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err == nil || !strings.Contains(err.Error(), "published candidate object") {
			t.Fatalf("candidate object proof error = %v", err)
		}
	})
}

func TestProveRetiredSourceBindsCandidateRejectsIntentMismatch(t *testing.T) {
	t.Parallel()
	candidate := strings.Repeat("a", 40)
	source := strings.Repeat("b", 40)
	tree := strings.Repeat("c", 40)
	cases := []struct {
		name     string
		gitQuery func(args []string) (string, error)
		mutate   func(*worktreeretire.Transaction)
		want     string
	}{
		{"parent mismatch", func(args []string) (string, error) {
			if len(args) == 2 && args[0] == "rev-parse" && args[1] == source+"^" {
				return strings.Repeat("d", 40), nil
			}
			return newRetiredProofFixtureDefaultGit(args)
		}, nil, "intent parent does not match"},
		{"parent read failure", func(args []string) (string, error) {
			if len(args) == 2 && args[0] == "rev-parse" && args[1] == source+"^" {
				return "", errors.New("parent read")
			}
			return newRetiredProofFixtureDefaultGit(args)
		}, nil, "intent parent does not match"},
		{"tree mismatch", func(args []string) (string, error) {
			if len(args) == 2 && args[0] == "rev-parse" && args[1] == source+"^{tree}" {
				return strings.Repeat("d", 40), nil
			}
			return newRetiredProofFixtureDefaultGit(args)
		}, nil, "intent tree does not match"},
		{"tree read failure", func(args []string) (string, error) {
			if len(args) == 2 && args[0] == "rev-parse" && args[1] == source+"^{tree}" {
				return "", errors.New("tree read")
			}
			return newRetiredProofFixtureDefaultGit(args)
		}, nil, "intent tree does not match"},
		{"message mismatch", func(args []string) (string, error) {
			if len(args) == 4 && args[0] == "log" {
				return "other message", nil
			}
			return newRetiredProofFixtureDefaultGit(args)
		}, nil, "intent message does not match"},
		{"message read failure", func(args []string) (string, error) {
			if len(args) == 4 && args[0] == "log" {
				return "", errors.New("message read")
			}
			return newRetiredProofFixtureDefaultGit(args)
		}, nil, "intent message does not match"},
		{"missing intent timestamp", func(args []string) (string, error) {
			return newRetiredProofFixtureDefaultGit(args)
		}, func(tx *worktreeretire.Transaction) { tx.IntentAt = time.Time{} }, "does not bind receipted candidate"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRetiredProofFixture(t)
			fixture.gitQuery = func(_ context.Context, _ string, args ...string) (string, error) {
				if len(args) == 2 && args[0] == "rev-parse" && args[1] == "FETCH_HEAD" {
					return source, nil
				}
				value, err := tt.gitQuery(args)
				if err != nil && strings.Contains(err.Error(), "unexpected git command") && len(args) == 2 && args[0] == "rev-parse" {
					switch args[1] {
					case source + "^":
						return candidate, nil
					case source + "^{tree}":
						return tree, nil
					}
				}
				return value, err
			}
			tx := fixture.tx
			tx.SourceSHA = source
			tx.IntentParentSHA = candidate
			tx.IntentTreeSHA = tree
			tx.IntentMessage = "Retire worktree source changes"
			tx.IntentAt = time.Now().UTC()
			if tt.mutate != nil {
				tt.mutate(&tx)
			}
			fixture.tx = tx
			if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("intent proof error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestProductionRetiredCheckoutProofRejectsNonCanonicalClone(t *testing.T) {
	t.Parallel()
	ports := productionRetiredCheckoutProofPorts()
	if _, err := ports.openCanonical(t.TempDir()); err == nil {
		t.Fatal("non-canonical directory accepted")
	}
}

func TestProveRetiredCheckoutBuildsConfiguredArchiveRemote(t *testing.T) {
	t.Parallel()
	fixture := newRetiredProofFixture(t)
	fixture.options.ArchiveRemote = ""
	archiveRemote := ""
	fixture.ports.verifyRemoved = func(_ context.Context, operation worktreeretire.Operation, _ worktreeretire.Transaction) error {
		archiveRemote = operation.ArchiveRemote
		return nil
	}
	if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err != nil {
		t.Fatal(err)
	}
	if archiveRemote != "git@github.com:"+fixture.tx.ArchiveRepository+".git" {
		t.Fatalf("archive remote = %q", archiveRemote)
	}
}

// TestProveRetiredCheckoutRefusesCanonicalIdentityReboundDuringProof proves
// that the descriptors opened for the initial canonical admission remain the
// authority: a root replacement, a .git replacement, or an ancestor symlink
// swap after admission refuses before any Git read or evidence is produced.
func TestProveRetiredCheckoutRefusesCanonicalIdentityReboundDuringProof(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"root replaced", "git directory replaced", "ancestor symlink"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fixture := newRetiredProofFixture(t)
			fixture.ports.afterCanonicalOpen = func() {
				moved := fixture.canonicalDir + "-moved"
				switch mode {
				case "root replaced":
					if err := os.Rename(fixture.canonicalDir, moved); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Join(fixture.canonicalDir, ".git"), 0o700); err != nil {
						t.Fatal(err)
					}
				case "git directory replaced":
					if err := os.Rename(filepath.Join(fixture.canonicalDir, ".git"), filepath.Join(fixture.canonicalDir, ".git-old")); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Join(fixture.canonicalDir, ".git"), 0o700); err != nil {
						t.Fatal(err)
					}
				case "ancestor symlink":
					if err := os.Rename(fixture.canonicalDir, moved); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(moved, fixture.canonicalDir); err != nil {
						t.Fatal(err)
					}
				}
			}
			evidence, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports)
			if err == nil || !strings.Contains(err.Error(), "authorize canonical clone") {
				t.Fatalf("%s proof = (%+v, %v), want canonical identity refusal", mode, evidence, err)
			}
			if evidence != (RetiredCheckoutEvidence{}) {
				t.Fatalf("%s produced evidence after rebound: %+v", mode, evidence)
			}
		})
	}
}

// TestProveRetiredCheckoutRefusesReceiptReplacedAfterSnapshot drives a real
// receipt-file replacement with controlled synchronization: the first secure
// snapshot is read and validated, the file is then swapped, and the final
// byte/digest comparison must refuse instead of labelling the new bytes with
// the old proof.
func TestProveRetiredCheckoutRefusesReceiptReplacedAfterSnapshot(t *testing.T) {
	t.Parallel()
	fixture := newRetiredProofFixture(t)
	home, err := fixture.ports.home("")
	if err != nil {
		t.Fatal(err)
	}
	fixture.tx.ReportPath = worktreeretire.ReportPath(home, worktreeretire.Transaction{Task: fixture.tx.Task, Repository: fixture.tx.Repository})
	fixture.tx.RetiredRef = worktreebranches.RetiredBranchDestination(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), fixture.tx.Branch, fixture.tx.SourceSHA)
	fixture.tx.ArchiveRef = worktreeretire.ArchiveRef(fixture.tx)
	if err := worktreeretire.WriteReport(fixture.tx); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixture.readSnapshot = func(path string) (worktreeretire.ReportSnapshot, error) {
		snapshot, err := worktreeretire.ReadReportSnapshot(path)
		if err != nil {
			return worktreeretire.ReportSnapshot{}, err
		}
		calls++
		if calls == 1 {
			changed := fixture.tx
			changed.Phase = "original_deleted"
			if err := worktreeretire.WriteReport(changed); err != nil {
				return worktreeretire.ReportSnapshot{}, err
			}
		}
		return snapshot, nil
	}
	if _, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports); err == nil || !strings.Contains(err.Error(), "changed while it was being proven") {
		t.Fatalf("receipt replacement proof = %v", err)
	}
	if calls != 2 {
		t.Fatalf("secure snapshot reads = %d, want 2", calls)
	}
}

// TestRetiredCheckoutProofSessionRefusesWithoutHeldIdentity covers the held
// session's own guards: a nil or descriptor-less proof can never return
// evidence, run Git, or claim validation, and closing it stays harmless.
func TestRetiredCheckoutProofSessionRefusesWithoutHeldIdentity(t *testing.T) {
	t.Parallel()
	var nilProof *RetiredCheckoutProof
	if evidence := nilProof.Evidence(); evidence != (RetiredCheckoutEvidence{}) {
		t.Fatalf("nil proof evidence = %+v", evidence)
	}
	if err := nilProof.Validate(); err == nil {
		t.Fatal("nil proof validated")
	}
	if _, err := nilProof.Git(context.Background(), "status"); err == nil {
		t.Fatal("nil proof ran Git")
	}
	if err := nilProof.Close(); err != nil {
		t.Fatalf("nil proof close = %v", err)
	}

	empty := &RetiredCheckoutProof{}
	if err := empty.Validate(); err == nil {
		t.Fatal("descriptor-less proof validated")
	}
	if _, err := empty.Git(context.Background(), "status"); err == nil {
		t.Fatal("descriptor-less proof ran Git")
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("descriptor-less proof close = %v", err)
	}
}

// proveRetiredCheckoutWith is the test-only evidence-only wrapper around the
// production held proof. Production callers must use ProveRetiredCheckoutHeld
// so the exact canonical session stays open through their later Git reads.
func proveRetiredCheckoutWith(ctx context.Context, options RetiredCheckoutProofOptions, ports retiredCheckoutProofPorts) (RetiredCheckoutEvidence, error) {
	proof, err := proveRetiredCheckoutHeldWith(ctx, options, ports)
	if err != nil {
		return RetiredCheckoutEvidence{}, err
	}
	defer func() { _ = proof.Close() }()
	return proof.Evidence(), nil
}

// TestRetiredCheckoutProofRefusesNamespaceAndReceiptChanges keeps the held
// proof bound to the descriptors and receipt bytes it admitted: a canonical
// root replaced before Validate, a receipt that cannot be re-read, a remote
// query whose held Git read fails, and an opener that admits a real repository
// whose lexical root was replaced before return all refuse, and the refused
// opener's descriptors are closed.
func TestRetiredCheckoutProofRefusesNamespaceAndReceiptChanges(t *testing.T) {
	t.Parallel()
	proved := func(t *testing.T) (*retiredProofFixture, *RetiredCheckoutProof) {
		t.Helper()
		fixture := newRetiredProofFixture(t)
		proof, err := proveRetiredCheckoutHeldWith(context.Background(), fixture.options, fixture.ports)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = proof.Close() })
		return fixture, proof
	}

	t.Run("canonical replaced before validate", func(t *testing.T) {
		t.Parallel()
		fixture, proof := proved(t)
		if err := os.Rename(fixture.canonicalDir, fixture.canonicalDir+"-held"); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(fixture.canonicalDir, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := proof.Validate(); err == nil || !strings.Contains(err.Error(), "canonical repository path changed") {
			t.Fatalf("validate after canonical replacement = %v", err)
		}
	})

	t.Run("receipt reread failure", func(t *testing.T) {
		t.Parallel()
		_, proof := proved(t)
		proof.readSnapshot = func(string) (worktreeretire.ReportSnapshot, error) {
			return worktreeretire.ReportSnapshot{}, errors.New("receipt unavailable")
		}
		if err := proof.Validate(); err == nil || !strings.Contains(err.Error(), "re-read retirement receipt") {
			t.Fatalf("validate after receipt failure = %v", err)
		}
	})

	t.Run("remote query failure", func(t *testing.T) {
		t.Parallel()
		_, proof := proved(t)
		childErr := errors.New("held git failed")
		proof.git = func(context.Context, *canonicalRepository, ...string) (string, error) {
			return "", childErr
		}
		if _, err := proof.remoteSHA(context.Background(), "origin", "refs/heads/topic"); err == nil ||
			!strings.Contains(err.Error(), "inspect remote ref") || !errors.Is(err, childErr) {
			t.Fatalf("remote query failure = %v", err)
		}
	})

	t.Run("opener returns a replaced namespace", func(t *testing.T) {
		t.Parallel()
		fixture := newRetiredProofFixture(t)
		var opened *canonicalRepository
		fixture.ports.openCanonical = func(string) (*canonicalRepository, error) {
			repository, err := openCanonicalRepository(fixture.canonicalDir)
			if err != nil {
				return nil, err
			}
			opened = repository
			if err := os.Rename(fixture.canonicalDir, fixture.canonicalDir+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(fixture.canonicalDir, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			return repository, nil
		}
		evidence, err := proveRetiredCheckoutWith(context.Background(), fixture.options, fixture.ports)
		if err == nil || !strings.Contains(err.Error(), "authorize canonical clone") {
			t.Fatalf("replaced opener = (%+v, %v)", evidence, err)
		}
		if evidence != (RetiredCheckoutEvidence{}) {
			t.Fatalf("replaced opener produced evidence: %+v", evidence)
		}
		if opened == nil {
			t.Fatal("opener never returned a repository")
		}
		if _, err := opened.root.Stat(); err == nil {
			t.Fatal("refused opener left its root descriptor open")
		}
	})
}

// TestRetiredCheckoutProofRefusesPostGitNamespaceSwap keeps the post-Git
// validation authoritative: a child that fails while the canonical namespace is
// swapped returns both errors, and a child that reports success cannot turn the
// same swap into a positive proof.
func TestRetiredCheckoutProofRefusesPostGitNamespaceSwap(t *testing.T) {
	t.Parallel()
	for _, childFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "child reports success", true: "child fails"}[childFails], func(t *testing.T) {
			t.Parallel()
			fixture := newRetiredProofFixture(t)
			proof, err := proveRetiredCheckoutHeldWith(context.Background(), fixture.options, fixture.ports)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = proof.Close() }()
			childErr := errors.New("held child failed")
			proof.git = func(context.Context, *canonicalRepository, ...string) (string, error) {
				if err := os.Rename(fixture.canonicalDir, fixture.canonicalDir+"-held"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(fixture.canonicalDir, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
				if childFails {
					return "", childErr
				}
				return "success output", nil
			}
			output, err := proof.Git(context.Background(), "status")
			if err == nil || !strings.Contains(err.Error(), "canonical repository path changed") {
				t.Fatalf("post-Git swap = (%q, %v)", output, err)
			}
			if output != "" {
				t.Fatalf("post-Git swap returned output as success: %q", output)
			}
			if childFails && !errors.Is(err, childErr) {
				t.Fatalf("post-Git swap dropped the child error: %v", err)
			}
		})
	}
}
