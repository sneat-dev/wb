//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitremote"
)

//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git and WB environment.
func TestE2ESessionReceiveCanonicalClonePreservesPublicationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		prepare    func(*testing.T, *sessionReceiveFixture, *os.File) string
		success    bool
		cancel     bool
		wrongID    bool
		postVerify bool
		noWrite    bool
		noGit      bool
	}{
		{name: "existing canonical", want: "already exists"},
		{name: "moved owner", prepare: func(t *testing.T, fixture *sessionReceiveFixture, _ *os.File) string {
			if err := os.Rename(filepath.Dir(fixture.canonical), filepath.Dir(fixture.canonical)+"-moved"); err != nil {
				t.Fatal(err)
			}
			return fixture.remote
		}, want: "owner path changed"},
		{name: "redacted clone failure", prepare: func(_ *testing.T, fixture *sessionReceiveFixture, _ *os.File) string {
			return filepath.Join(fixture.root, "sensitive-credential-no-such-remote")
		}, want: "secure clone of canonical repository"},
		{name: "canceled stage verification", cancel: true, want: "secure staging directory"},
		{name: "stage creation permission", noWrite: true, want: "create secure canonical clone stage"},
		{name: "trusted Git unavailable", noGit: true, want: "trusted Git unavailable"},
		{name: "wrong staged origin identity", wrongID: true, want: "verify staged canonical clone"},
		{name: "published verification rollback", postVerify: true, want: "verify published canonical clone"},
		{name: "successful clone", success: true},
	} {
		//nolint:paralleltest // each fixture sets process-wide Git and WB environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			if tc.name != "existing canonical" {
				if err := os.RemoveAll(fixture.canonical); err != nil {
					t.Fatal(err)
				}
			}
			ownerPath := filepath.Dir(fixture.canonical)
			owner, err := os.Open(ownerPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := owner.Close(); err != nil {
					t.Errorf("close owner directory: %v", err)
				}
			}()
			declaredRemote := fixture.remote
			if tc.prepare != nil {
				declaredRemote = tc.prepare(t, fixture, owner)
			}
			if tc.noWrite {
				if err := os.Chmod(ownerPath, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(ownerPath, 0o700) })
			}
			if tc.noGit {
				original := trustedGitExecutableFn
				trustedGitExecutableFn = func() (string, error) { return "", errors.New("trusted Git unavailable") }
				t.Cleanup(func() { trustedGitExecutableFn = original })
			}
			parsed, err := gitremote.Parse(fixture.remote)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wrongID {
				parsed.Identity.Repository = "acme/other"
			}
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			seenPublishedVerification := false
			if tc.postVerify {
				rootQueries := 0
				ctx = withCanonicalGitInterceptor(ctx, func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
					if len(args) == 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
						rootQueries++
						if rootQueries == 2 {
							seenPublishedVerification = true
							return []byte(t.TempDir()), nil
						}
					}
					return run()
				})
			}
			canonical, err := cloneSessionReceiveCanonical(ctx, owner, ownerPath, "app", fixture.canonical, declaredRemote, parsed.Identity)
			if tc.postVerify && !seenPublishedVerification {
				t.Fatal("published verification was never reached")
			}
			if canonical != nil {
				defer canonical.close()
			}
			if tc.success {
				if err != nil || canonical == nil {
					t.Fatalf("clone = %v, %v", canonical, err)
				}
				if got := gitTestOutput(t, fixture.canonical, "remote", "get-url", "origin"); got != fixture.remote {
					t.Fatalf("published origin = %q", got)
				}
				return
			}
			if canonical != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("clone = %v, %v; want %q", canonical, err, tc.want)
			}
			if strings.Contains(err.Error(), "sensitive-credential") {
				t.Fatalf("clone error leaked remote: %v", err)
			}
			if tc.name != "existing canonical" {
				if _, statErr := os.Lstat(fixture.canonical); !os.IsNotExist(statErr) {
					t.Fatalf("canonical published on failure: %v", statErr)
				}
			}
		})
	}
}

//nolint:paralleltest // each real Git fixture sets process-wide Git and WB environment.
func TestE2ESessionReceiveInterruptedRecoveryRefusesUnboundStates(t *testing.T) {
	for _, tc := range []struct {
		name, want  string
		prepare     func(*testing.T, *sessionReceiveFixture, string, string, string)
		finalExists bool
		wrongCommit bool
		gitFailure  bool
		reused      bool
		badParent   bool
		wrongPath   bool
		badPostList bool
	}{
		{name: "no pin and no target"},
		{name: "target without pin", finalExists: true, want: "without its exact pin registration"},
		{name: "pin inspection failure", gitFailure: true, want: "inspect interrupted target pin registration"},
		{name: "pin names missing target", prepare: registerFinalSessionPin, want: "names a missing deterministic target"},
		{name: "published pin has wrong commit", prepare: registerFinalSessionPin, finalExists: true, wrongCommit: true, want: "verify interrupted published target"},
		{name: "published pin retires empty stage", prepare: registerFinalSessionPin, finalExists: true, reused: true},
		{name: "published pin refuses nonempty stage", prepare: registerFinalPinWithNonemptyStage, finalExists: true, want: "not empty"},
		{name: "both staged and published", prepare: registerStagedSessionPin, finalExists: true, want: "both staged and published checkouts"},
		{name: "symlinked active stage", prepare: registerSymlinkedStagedPin, want: "open exact interrupted receive stage"},
		{name: "invalid owner parent", prepare: registerOnlyStagedPin, badParent: true, want: "invalid secure worktree parent"},
		{name: "occupied final destination", prepare: registerStagedSessionPin, want: "already exists"},
		{name: "symlinked staged checkout", prepare: registerSymlinkedCheckoutPin, finalExists: true, want: "inspect interrupted staged checkout"},
		{name: "published path mismatch", prepare: registerMovedStagedPin, finalExists: true, wrongPath: true, want: "published target path changed"},
		{name: "published checkout wrong commit", prepare: registerMovedStagedPin, finalExists: true, wrongCommit: true, want: "verify interrupted published checkout"},
		{name: "registration changed after repair", prepare: registerMovedStagedPin, finalExists: true, badPostList: true, want: "registration was not repaired"},
	} {
		//nolint:paralleltest // each fixture sets process-wide Git and WB environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			canonical := mustOpenCanonical(t, fixture.canonical)
			defer canonical.close()
			spec := sessionReceiveStateSpec(fixture)
			_, operationRoot, parent, name, finalPath, err := sessionReceivePhysicalCoordinates(context.Background(), fixture.projectsRoot, canonical, spec, "acme/app")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(operationRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil {
				tc.prepare(t, fixture, operationRoot, finalPath, spec.PinBranch)
			}
			operationDirectory, err := os.Open(operationRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := operationDirectory.Close(); err != nil {
					t.Errorf("close operation directory: %v", err)
				}
			}()
			ctx := context.Background()
			if tc.gitFailure || tc.badPostList {
				listCalls := 0
				ctx = withCanonicalGitInterceptor(ctx, func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
					if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
						listCalls++
						if tc.gitFailure {
							return nil, errors.New("pin inspection failed")
						}
						if listCalls == 2 {
							return nil, nil
						}
					}
					return run()
				})
			}
			commit := spec.Commit
			if tc.wrongCommit {
				commit = strings.Repeat("f", 40)
			}
			if tc.badParent {
				parent = "../unsafe"
			}
			if tc.wrongPath {
				finalPath += "-wrong"
			}
			reused, err := recoverInterruptedSessionReceivePublication(ctx, canonical, operationRoot, operationDirectory, parent, name, finalPath, spec.PinBranch, commit, tc.finalExists)
			if tc.want == "" && (err != nil || reused != tc.reused) {
				t.Fatalf("unbound recovery = %t, %v", reused, err)
			}
			if tc.want != "" && (reused || err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("unbound recovery = %t, %v; want %q", reused, err, tc.want)
			}
		})
	}
}

func registerFinalSessionPin(t *testing.T, fixture *sessionReceiveFixture, _, finalPath, pinBranch string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", pinBranch, finalPath, fixture.request.BundleCommit)
}

func registerStagedSessionPin(t *testing.T, fixture *sessionReceiveFixture, operationRoot, finalPath, pinBranch string) {
	t.Helper()
	registerOnlyStagedPin(t, fixture, operationRoot, finalPath, pinBranch)
	if err := os.MkdirAll(finalPath, 0o700); err != nil {
		t.Fatal(err)
	}
}

func registerOnlyStagedPin(t *testing.T, fixture *sessionReceiveFixture, operationRoot, _ string, pinBranch string) {
	t.Helper()
	stage := filepath.Join(operationRoot, ".wb-stage-0123456789abcdef0123456789abcdef")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", pinBranch, filepath.Join(stage, "checkout"), fixture.request.BundleCommit)
}

func registerFinalPinWithNonemptyStage(t *testing.T, fixture *sessionReceiveFixture, operationRoot, finalPath, pinBranch string) {
	t.Helper()
	registerFinalSessionPin(t, fixture, operationRoot, finalPath, pinBranch)
	stage := filepath.Join(operationRoot, ".wb-stage-0123456789abcdef0123456789abcdef")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "retained"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func registerSymlinkedStagedPin(t *testing.T, fixture *sessionReceiveFixture, operationRoot, finalPath, pinBranch string) {
	t.Helper()
	registerOnlyStagedPin(t, fixture, operationRoot, finalPath, pinBranch)
	stage := filepath.Join(operationRoot, ".wb-stage-0123456789abcdef0123456789abcdef")
	if err := os.Rename(stage, stage+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stage+"-moved", stage); err != nil {
		t.Fatal(err)
	}
}

func registerSymlinkedCheckoutPin(t *testing.T, fixture *sessionReceiveFixture, operationRoot, finalPath, pinBranch string) {
	t.Helper()
	registerOnlyStagedPin(t, fixture, operationRoot, finalPath, pinBranch)
	stage := filepath.Join(operationRoot, ".wb-stage-0123456789abcdef0123456789abcdef")
	checkout := filepath.Join(stage, "checkout")
	if err := os.Rename(checkout, checkout+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checkout+"-moved", checkout); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(finalPath, 0o700); err != nil {
		t.Fatal(err)
	}
}

func registerMovedStagedPin(t *testing.T, fixture *sessionReceiveFixture, operationRoot, finalPath, pinBranch string) {
	t.Helper()
	registerOnlyStagedPin(t, fixture, operationRoot, finalPath, pinBranch)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(operationRoot, ".wb-stage-0123456789abcdef0123456789abcdef")
	if err := os.Rename(filepath.Join(stage, "checkout"), finalPath); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // every case creates a real Git fixture with process-wide environment.
func TestE2ESessionReceiveCanonicalCloneFaultBoundaries(t *testing.T) {
	marker := errors.New("clone boundary failed")
	var movedDescriptor, canonicalRoot *os.File
	for _, tc := range []struct {
		name, want string
		published  bool
		change     func(*testing.T, *sessionReceivePublicationPorts, string)
	}{
		{name: "open stage", want: "clone boundary failed", change: func(_ *testing.T, p *sessionReceivePublicationPorts, _ string) {
			original := p.openDirectory
			p.openDirectory = func(fd int, name, descriptor, openContext, wrapContext string) (*os.File, error) {
				if descriptor == "wb-session-receive-clone-stage" {
					return nil, marker
				}
				return original(fd, name, descriptor, openContext, wrapContext)
			}
		}},
		{name: "second stage verification", want: "clone boundary failed", change: func(_ *testing.T, p *sessionReceivePublicationPorts, _ string) {
			original, calls := p.verifyStage, 0
			p.verifyStage = func(ctx context.Context, stage *os.File, root string) error {
				calls++
				if calls == 2 {
					return marker
				}
				return original(ctx, stage, root)
			}
		}},
		{name: "stage path", want: "clone boundary failed", change: func(_ *testing.T, p *sessionReceivePublicationPorts, _ string) {
			p.stagePath = func(context.Context, *os.File) (string, error) { return "", marker }
		}},
		{name: "open staged checkout", want: "clone boundary failed", change: func(_ *testing.T, p *sessionReceivePublicationPorts, _ string) {
			original := p.openDirectory
			p.openDirectory = func(fd int, name, descriptor, openContext, wrapContext string) (*os.File, error) {
				if descriptor == "wb-session-receive-staged-canonical" {
					return nil, marker
				}
				return original(fd, name, descriptor, openContext, wrapContext)
			}
		}},
		{name: "retain staged checkout", want: "retain staged canonical clone", change: func(_ *testing.T, p *sessionReceivePublicationPorts, _ string) {
			p.openHeld = func(string, *os.File) (*canonicalRepository, error) { return nil, marker }
		}},
		{name: "owner moved before publication", want: "owner path changed before publishing", change: func(t *testing.T, p *sessionReceivePublicationPorts, ownerPath string) {
			original := p.verifyCanonical
			p.verifyCanonical = func(ctx context.Context, canonical *canonicalRepository, declared gitremote.Identity) error {
				if err := original(ctx, canonical, declared); err != nil {
					return err
				}
				if err := os.Rename(ownerPath, ownerPath+"-moved"); err != nil {
					t.Fatal(err)
				}
				return nil
			}
		}},
		{name: "move reports retained published descriptor", want: "publish canonical clone", published: true, change: func(_ *testing.T, p *sessionReceivePublicationPorts, _ string) {
			original := p.move
			p.move = func(from *os.File, fromName string, to *os.File, toName string, expected *os.File, after func(), more ...func()) (*os.File, error) {
				published, err := original(from, fromName, to, toName, expected, after, more...)
				if err != nil {
					return published, err
				}
				movedDescriptor = published
				return published, marker
			}
		}},
		{name: "retain published checkout", want: "verify published canonical clone", change: func(_ *testing.T, p *sessionReceivePublicationPorts, _ string) {
			original, calls := p.openHeld, 0
			p.openHeld = func(path string, held *os.File) (*canonicalRepository, error) {
				calls++
				if calls == 2 {
					return nil, marker
				}
				return original(path, held)
			}
		}},
		{name: "quarantine after successful publication", want: "retire secure canonical clone stage", published: true, change: func(_ *testing.T, p *sessionReceivePublicationPorts, _ string) {
			original, opens := p.openHeld, 0
			p.openHeld = func(path string, held *os.File) (*canonicalRepository, error) {
				opened, err := original(path, held)
				opens++
				if opens == 2 && opened != nil {
					canonicalRoot = opened.root
				}
				return opened, err
			}
			p.quarantine = func(*os.File, *os.File) error { return marker }
		}},
	} {
		//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git and WB environment.
		t.Run(tc.name, func(t *testing.T) {
			movedDescriptor, canonicalRoot = nil, nil
			fixture := newSessionReceiveFixture(t)
			if err := os.RemoveAll(fixture.canonical); err != nil {
				t.Fatal(err)
			}
			ownerPath := filepath.Dir(fixture.canonical)
			owner, err := os.Open(ownerPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := owner.Close(); err != nil {
					t.Errorf("close owner directory: %v", err)
				}
			}()
			parsed, err := gitremote.Parse(fixture.remote)
			if err != nil {
				t.Fatal(err)
			}
			ports := productionSessionReceivePublicationPorts()
			tc.change(t, &ports, ownerPath)
			canonical, err := cloneSessionReceiveCanonicalWithPorts(context.Background(), owner, ownerPath, "app", fixture.canonical, fixture.remote, parsed.Identity, ports)
			if canonical != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("clone fault = %v, %v; want %q", canonical, err, tc.want)
			}
			if tc.name != "owner moved before publication" {
				_, statErr := os.Lstat(fixture.canonical)
				if tc.published && statErr != nil {
					t.Fatalf("completed physical publication lost: %v", statErr)
				}
				if !tc.published && !os.IsNotExist(statErr) {
					t.Fatalf("canonical published before refusal: %v", statErr)
				}
			}
			if tc.name == "move reports retained published descriptor" {
				if movedDescriptor == nil {
					t.Fatal("move did not return a retained descriptor")
				}
				if _, statErr := movedDescriptor.Stat(); !errors.Is(statErr, os.ErrClosed) {
					t.Fatalf("retained moved descriptor remained open: %v", statErr)
				}
			}
			if tc.name == "quarantine after successful publication" {
				if canonicalRoot == nil {
					t.Fatal("published canonical root was not captured")
				}
				if _, statErr := canonicalRoot.Stat(); !errors.Is(statErr, os.ErrClosed) {
					t.Fatalf("published canonical root remained open: %v", statErr)
				}
			}
		})
	}
}

//nolint:paralleltest // each case uses a real Git fixture and process-wide configuration.
func TestE2ESessionReceiveInterruptedRecoveryFaultBoundaries(t *testing.T) {
	marker := errors.New("recovery boundary failed")
	var movedDescriptor *os.File
	for _, tc := range []struct {
		name, want string
		moved      bool
		published  bool
		change     func(*testing.T, *sessionReceivePublicationPorts)
	}{
		{name: "open exact stage", want: "recovery boundary failed", change: func(_ *testing.T, p *sessionReceivePublicationPorts) {
			original := p.openDirectory
			p.openDirectory = func(fd int, name, descriptor, openContext, wrapContext string) (*os.File, error) {
				if descriptor == "wb-session-receive-interrupted-stage" {
					return nil, marker
				}
				return original(fd, name, descriptor, openContext, wrapContext)
			}
		}},
		{name: "stage descriptor mismatch", want: "stage path changed", change: func(t *testing.T, p *sessionReceivePublicationPorts) {
			original := p.openDirectory
			p.openDirectory = func(fd int, name, descriptor, openContext, wrapContext string) (*os.File, error) {
				if descriptor == "wb-session-receive-interrupted-stage" {
					return os.Open(t.TempDir())
				}
				return original(fd, name, descriptor, openContext, wrapContext)
			}
		}},
		{name: "stage verification", want: "verify exact interrupted receive stage", change: func(_ *testing.T, p *sessionReceivePublicationPorts) {
			p.verifyStage = func(context.Context, *os.File, string) error { return marker }
		}},
		{name: "owner descriptor mismatch", want: "owner path changed", change: func(t *testing.T, p *sessionReceivePublicationPorts) {
			original := p.openParent
			p.openParent = func(root *os.File, path, parent string) (*os.File, string, error) {
				held, _, err := original(root, path, parent)
				return held, t.TempDir(), err
			}
		}},
		{name: "staged checkout descriptor mismatch", want: "no longer matches its exact Git registration", change: func(t *testing.T, p *sessionReceivePublicationPorts) {
			original := p.openDirectory
			p.openDirectory = func(fd int, name, descriptor, openContext, wrapContext string) (*os.File, error) {
				if descriptor == "wb-session-receive-interrupted-checkout" {
					return os.Open(t.TempDir())
				}
				return original(fd, name, descriptor, openContext, wrapContext)
			}
		}},
		{name: "move reports retained published descriptor", want: "publish exact interrupted staged checkout", published: true, change: func(_ *testing.T, p *sessionReceivePublicationPorts) {
			original := p.move
			p.move = func(from *os.File, fromName string, to *os.File, toName string, expected *os.File, after func(), more ...func()) (*os.File, error) {
				published, err := original(from, fromName, to, toName, expected, after, more...)
				if err != nil {
					return published, err
				}
				movedDescriptor = published
				return published, marker
			}
		}},
		{name: "registration repair", moved: true, want: "repair exact interrupted target registration", change: func(_ *testing.T, p *sessionReceivePublicationPorts) {
			p.repair = func(context.Context, *canonicalRepository, *os.File, *os.File, string, string, ...string) error {
				return marker
			}
		}},
		{name: "post repair checkout proof", moved: true, want: "verify exact interrupted target after repair", change: func(_ *testing.T, p *sessionReceivePublicationPorts) {
			original, calls := p.verifyHeld, 0
			p.verifyHeld = func(ctx context.Context, canonical, operationRoot, path string, held *os.File, pin, commit string) error {
				calls++
				if calls == 2 {
					return marker
				}
				return original(ctx, canonical, operationRoot, path, held, pin, commit)
			}
		}},
		{name: "retire repaired stage", moved: true, want: "retire recovered interrupted receive stage", change: func(_ *testing.T, p *sessionReceivePublicationPorts) {
			p.quarantine = func(*os.File, *os.File) error { return marker }
		}},
	} {
		//nolint:paralleltest // newSessionReceiveFixture sets process-wide Git and WB environment.
		t.Run(tc.name, func(t *testing.T) {
			movedDescriptor = nil
			fixture := newSessionReceiveFixture(t)
			canonical := mustOpenCanonical(t, fixture.canonical)
			defer canonical.close()
			spec := sessionReceiveStateSpec(fixture)
			_, operationRoot, parent, name, finalPath, err := sessionReceivePhysicalCoordinates(context.Background(), fixture.projectsRoot, canonical, spec, "acme/app")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(operationRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.moved {
				registerMovedStagedPin(t, fixture, operationRoot, finalPath, spec.PinBranch)
			} else {
				registerOnlyStagedPin(t, fixture, operationRoot, finalPath, spec.PinBranch)
			}
			operationDirectory, err := os.Open(operationRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := operationDirectory.Close(); err != nil {
					t.Errorf("close operation directory: %v", err)
				}
			}()
			ports := productionSessionReceivePublicationPorts()
			tc.change(t, &ports)
			reused, err := recoverInterruptedSessionReceivePublicationWithPorts(context.Background(), canonical, operationRoot, operationDirectory, parent, name, finalPath, spec.PinBranch, spec.Commit, tc.moved, ports)
			if reused || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("recovery fault = %t, %v; want %q", reused, err, tc.want)
			}
			if tc.published {
				if _, statErr := os.Stat(finalPath); statErr != nil {
					t.Fatalf("published checkout lost after move error: %v", statErr)
				}
				if movedDescriptor == nil {
					t.Fatal("move did not return a retained descriptor")
				}
				if _, statErr := movedDescriptor.Stat(); !errors.Is(statErr, os.ErrClosed) {
					t.Fatalf("retained moved descriptor remained open: %v", statErr)
				}
			}
		})
	}
}
