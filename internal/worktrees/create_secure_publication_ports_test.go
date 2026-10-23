package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func fakeSecurePublication(t *testing.T) (securePublicationRequest, func()) {
	t.Helper()
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var publication *createdWorktreePublication
	request := securePublicationRequest{
		operationRoot: root, operationDirectory: directory, repository: "task", branch: "feature", base: "main",
		baseRevision: strings.Repeat("a", 40), publication: &publication,
		ops: securePublicationOps{
			registrations: func(context.Context, *canonicalRepository) (map[string]bool, error) { return map[string]bool{}, nil },
			acquireLock: func(*canonicalRepository) (*repositoryRegistrationLock, error) {
				return &repositoryRegistrationLock{}, nil
			},
			releaseLock: func(*repositoryRegistrationLock) error { return nil },
			gitAdd: func(_ context.Context, _ *canonicalRepository, _ string, stage *os.File, _, _ string, _ bool) error {
				return unix.Mkdirat(int(stage.Fd()), "checkout", 0o700)
			},
			verifyStage: func(context.Context, *os.File, string) error { return nil },
			repair:      func(context.Context, *canonicalRepository, *os.File, *os.File, string, string) error { return nil },
			verifyPublished: func(context.Context, *canonicalRepository, map[string]bool, string, *os.File, string, *os.File, string) error {
				return nil
			},
			rollbackCreated: func(context.Context, *canonicalRepository, *os.File, *os.File, map[string]bool, string, string, string) error {
				return nil
			},
			head: func(context.Context, *canonicalRepository, string) (string, error) {
				return strings.Repeat("b", 40), nil
			},
		},
	}
	return request, func() {
		publication.close()
		_ = directory.Close()
	}
}

func TestSecurePublicationReportsPreparationAndRollbackFaults(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		want   string
		modify func(*securePublicationRequest)
	}{
		{"missing receipt", "publication receipt is required", func(r *securePublicationRequest) { r.publication = nil }},
		{"invalid new branch base", "verified base commit is invalid", func(r *securePublicationRequest) { r.baseRevision = "bad" }},
		{"secure operation path", "resolve secure worktree operation directory", func(r *securePublicationRequest) {
			r.ops.securePath = func(context.Context, *os.File) (string, error) { return "", errors.New("path unavailable") }
		}},
		{"registration snapshot", "registrations unavailable", func(r *securePublicationRequest) {
			r.ops.registrations = func(context.Context, *canonicalRepository) (map[string]bool, error) {
				return nil, errors.New("registrations unavailable")
			}
		}},
		{"parent descriptor", "parent unavailable", func(r *securePublicationRequest) {
			r.ops.openParent = func(*os.File, string, string) (*os.File, string, error) {
				return nil, "", errors.New("parent unavailable")
			}
		}},
		{"stage creation", "create secure worktree staging directory", func(r *securePublicationRequest) {
			r.ops.makeStage = func(*os.File, string, bool) (string, error) { return "", errors.New("stage unavailable") }
		}},
		{"stage identity", "inspect secure worktree staging directory", func(r *securePublicationRequest) {
			r.ops.stageIdentity = func(int, string) (secureDirectoryIdentity, error) {
				return secureDirectoryIdentity{}, errors.New("identity unavailable")
			}
		}},
		{"destination occupied", "destination already exists", func(r *securePublicationRequest) {
			if err := os.Mkdir(filepath.Join(r.operationRoot, r.repository), 0o700); err != nil {
				panic(err)
			}
		}},
		{"stage open", "stage open unavailable", func(r *securePublicationRequest) {
			r.ops.openDirectory = func(int, string, string, string, string) (*os.File, error) {
				return nil, errors.New("stage open unavailable")
			}
		}},
		{"staged checkout open", "checkout open unavailable", func(r *securePublicationRequest) {
			r.ops.openDirectory = func(parent int, name, label, openContext, wrapContext string) (*os.File, error) {
				if name == "checkout" {
					return nil, errors.New("checkout open unavailable")
				}
				return openDirectoryAtNoFollow(parent, name, label, openContext, wrapContext)
			}
		}},
		{"stage path changed", "staging directory path changed", func(r *securePublicationRequest) {
			r.ops.matches = func(string, *os.File) bool { return false }
		}},
		{"registration lock", "acquire repository registration lock", func(r *securePublicationRequest) {
			r.ops.acquireLock = func(*canonicalRepository) (*repositoryRegistrationLock, error) {
				return nil, errors.New("lock unavailable")
			}
		}},
		{"stage verification", "verify secure staging directory before publish", func(r *securePublicationRequest) {
			r.ops.verifyStage = func(context.Context, *os.File, string) error { return errors.New("verification unavailable") }
		}},
		{"owner changed before publish", "owner path changed during creation", func(r *securePublicationRequest) {
			calls := 0
			r.ops.matches = func(path string, file *os.File) bool {
				calls++
				if calls == 2 {
					return false
				}
				return directoryStillMatches(path, file)
			}
		}},
		{"publish move refusal", "publish secure worktree", func(r *securePublicationRequest) {
			r.ops.move = func(*os.File, string, *os.File, string, *os.File, func(), ...func()) (*os.File, error) {
				return nil, errors.New("move unavailable")
			}
		}},
		{"ambiguous publish move", "ambiguous replacement state", func(r *securePublicationRequest) {
			r.ops.move = func(*os.File, string, *os.File, string, *os.File, func(), ...func()) (*os.File, error) {
				return &os.File{}, errDirectoryMoveIdentityChanged
			}
		}},
		{"rollback failure", "rollback incomplete worktree creation", func(r *securePublicationRequest) {
			r.ops.acquireLock = func(*canonicalRepository) (*repositoryRegistrationLock, error) {
				return nil, errors.New("lock unavailable")
			}
			r.ops.rollbackCreated = func(context.Context, *canonicalRepository, *os.File, *os.File, map[string]bool, string, string, string) error {
				return errors.New("rollback unavailable")
			}
		}},
		{"lock release during rollback", "release repository registration lock before rollback", func(r *securePublicationRequest) {
			r.ops.releaseLock = func(*repositoryRegistrationLock) error { return errors.New("unlock unavailable") }
			r.ops.gitAdd = func(context.Context, *canonicalRepository, string, *os.File, string, string, bool) error {
				return errors.New("Git add unavailable")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request, close := fakeSecurePublication(t)
			t.Cleanup(close)
			test.modify(&request)
			err := addWorktreeAtSecureDestination(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s error = %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestSecurePublicationPreservesSubstitutedStagedCheckout(t *testing.T) {
	t.Parallel()
	request, close := fakeSecurePublication(t)
	t.Cleanup(close)
	stagePath := ""
	request.ops.makeStage = func(parent *os.File, _ string, _ bool) (string, error) {
		name, err := makeSecureStageDirectory(parent)
		stagePath = filepath.Join(request.operationRoot, name)
		return name, err
	}
	request.hooks.afterStagedAdd = func() error {
		if err := os.Rename(filepath.Join(stagePath, "checkout"), filepath.Join(stagePath, "original")); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(stagePath, "checkout"), 0o700); err != nil {
			return err
		}
		return errors.New("checkout substituted")
	}
	err := addWorktreeAtSecureDestination(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "checkout substituted") {
		t.Fatalf("substitution refusal = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagePath, "checkout")); err != nil {
		t.Fatalf("substituted checkout removed: %v", err)
	}
}

func TestSecurePublicationRefusesPostPublishIdentityAndRetentionFaults(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, want string
		modify     func(*securePublicationRequest)
	}{
		{"owner changed", "owner path changed after publish", func(r *securePublicationRequest) {
			calls := 0
			r.ops.matches = func(path string, file *os.File) bool {
				calls++
				if calls == 3 {
					return false
				}
				return directoryStillMatches(path, file)
			}
		}},
		{"worktree changed", "published worktree path changed before repair", func(r *securePublicationRequest) {
			calls := 0
			r.ops.matches = func(path string, file *os.File) bool {
				calls++
				if calls == 4 {
					return false
				}
				return directoryStillMatches(path, file)
			}
		}},
		{"registration release", "release repository registration lock", func(r *securePublicationRequest) {
			r.ops.releaseLock = func(*repositoryRegistrationLock) error { return errors.New("unlock unavailable") }
		}},
		{"owner descriptor", "retain published worktree owner", func(r *securePublicationRequest) {
			r.ops.duplicate = func(*os.File, string) (*os.File, error) { return nil, errors.New("dup unavailable") }
		}},
		{"worktree descriptor", "retain published worktree identity", func(r *securePublicationRequest) {
			calls := 0
			r.ops.duplicate = func(file *os.File, name string) (*os.File, error) {
				calls++
				if calls == 2 {
					return nil, errors.New("dup unavailable")
				}
				return duplicateDirectoryDescriptor(file, name)
			}
		}},
		{"head lookup", "retain published branch head", func(r *securePublicationRequest) {
			r.ops.head = func(context.Context, *canonicalRepository, string) (string, error) {
				return "", errors.New("head unavailable")
			}
		}},
		{"invalid head", "Git returned invalid commit", func(r *securePublicationRequest) {
			r.ops.head = func(context.Context, *canonicalRepository, string) (string, error) { return "invalid", nil }
		}},
		{"repair refusal", "repair published worktree metadata", func(r *securePublicationRequest) {
			r.ops.repair = func(context.Context, *canonicalRepository, *os.File, *os.File, string, string) error {
				return errors.New("repair unavailable")
			}
		}},
		{"verification refusal", "verify published worktree after repair", func(r *securePublicationRequest) {
			r.ops.verifyPublished = func(context.Context, *canonicalRepository, map[string]bool, string, *os.File, string, *os.File, string) error {
				return errors.New("verification unavailable")
			}
		}},
		{"rollback move refusal", "roll back published checkout", func(r *securePublicationRequest) {
			r.ops.repair = func(context.Context, *canonicalRepository, *os.File, *os.File, string, string) error {
				return errors.New("repair unavailable")
			}
			calls := 0
			r.ops.move = func(from *os.File, fromName string, to *os.File, toName string, expected *os.File, after func(), afterMove ...func()) (*os.File, error) {
				calls++
				if calls == 2 {
					return nil, errors.New("reverse move unavailable")
				}
				return moveExpectedDirectoryNoReplace(from, fromName, to, toName, expected, after, afterMove...)
			}
		}},
		{"ambiguous rollback move", "ambiguous replacement state", func(r *securePublicationRequest) {
			r.ops.repair = func(context.Context, *canonicalRepository, *os.File, *os.File, string, string) error {
				return errors.New("repair unavailable")
			}
			calls := 0
			r.ops.move = func(from *os.File, fromName string, to *os.File, toName string, expected *os.File, after func(), afterMove ...func()) (*os.File, error) {
				calls++
				if calls == 2 {
					return &os.File{}, errDirectoryMoveIdentityChanged
				}
				return moveExpectedDirectoryNoReplace(from, fromName, to, toName, expected, after, afterMove...)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request, close := fakeSecurePublication(t)
			t.Cleanup(close)
			test.modify(&request)
			err := addWorktreeAtSecureDestination(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s error = %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestSecurePublicationKeepsRetainedDescriptorOnSuccess(t *testing.T) {
	t.Parallel()
	request, close := fakeSecurePublication(t)
	t.Cleanup(close)
	if err := addWorktreeAtSecureDestination(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if *request.publication == nil || (*request.publication).headSHA != strings.Repeat("b", 40) ||
		!directoryStillMatches(filepath.Join(request.operationRoot, request.repository), (*request.publication).worktreeDirectory) {
		t.Fatalf("published checkout descriptor = %+v", *request.publication)
	}
}

func TestSecurePublicationCallsExplicitBoundaryHooks(t *testing.T) {
	t.Parallel()
	request, close := fakeSecurePublication(t)
	t.Cleanup(close)
	var calls []string
	callback := func(name string) func() { return func() { calls = append(calls, name) } }
	request.hooks = securePublicationHooks{
		beforeAdd:                     callback("before add"),
		afterStageDirectoryCreated:    callback("stage created"),
		afterStageValidation:          callback("stage validated"),
		afterRegistrationLockAcquired: callback("registration locked"),
		beforeStagedWorktreeOpen:      callback("before staged open"),
		afterStagedAdd:                func() error { calls = append(calls, "staged add"); return nil },
		afterStageVerification:        callback("stage verified"),
		afterDestinationValidation:    callback("destination validated"),
		afterCheckoutAuthorization:    callback("checkout authorized"),
		afterCheckoutMove:             callback("checkout moved"),
		afterPublishedAuthorization:   callback("published authorized"),
		afterRepair:                   callback("repair finished"),
	}
	if err := addWorktreeAtSecureDestination(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(calls, ","), "stage created,before add,stage validated,registration locked,before staged open,staged add,stage verified,destination validated,checkout authorized,checkout moved,published authorized,repair finished"; got != want {
		t.Fatalf("publication callbacks = %q, want %q", got, want)
	}
}

func TestSecurePublicationCanUseCallerRepair(t *testing.T) {
	t.Parallel()
	request, close := fakeSecurePublication(t)
	t.Cleanup(close)
	called := false
	request.hooks.beforeRepair = func() error { called = true; return nil }
	if err := addWorktreeAtSecureDestination(context.Background(), request); err != nil || !called {
		t.Fatalf("caller repair called=%t err=%v", called, err)
	}
}
