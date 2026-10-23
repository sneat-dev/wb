package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/pathguard"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func fakeCreateOptions(t *testing.T) CreateOptions {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, ".wb")
	store := filepath.Join(home, "worktrees")
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	options := CreateOptions{ProjectsRoot: root, Operation: "task", WorkLog: WorkLogOptions{Model: "unknown"}}
	options.ports = createWorkflowPorts{
		gitCapability: func() error { return nil },
		resolveHome: func(string) (wbhome.Resolution, error) {
			return wbhome.Resolution{Root: root, Write: wbhome.Layout{Home: home, WorktreesRoot: store}}, nil
		},
		storePolicy: func(string) (userStorePolicy, error) { return userStorePolicy{CentralRoot: store}, nil },
		writableRequirements: func(_, _ string, repositories []string, _ userStorePolicy, _ bool) ([]string, []pathguard.Requirement, error) {
			paths := make([]string, len(repositories))
			for i, repository := range repositories {
				paths[i] = filepath.Join(root, "canonical", repository)
			}
			return paths, nil, nil
		},
		checkWritable: func(string, []pathguard.Requirement, pathguard.Probe) error { return nil },
		prepareLog:    func(_, _ string, options WorkLogOptions) (WorkLogOptions, error) { return options, nil },
		reserveLog:    func(string, string, WorkLogOptions) error { return nil },
		prepareOperation: func(_, operation string, _ func()) (preparedOperationRoot, error) {
			return preparedOperationRoot{Path: filepath.Join(store, operation)}, nil
		},
		acquireLock:   func(*os.File, string) (operationLock, error) { return operationLock{}, nil },
		openCanonical: func(path string) (*canonicalRepository, error) { return &canonicalRepository{path: path}, nil },
		userPlacement: func(_ context.Context, _ userStorePolicy, _, canonical string) (WorktreePlacement, error) {
			return WorktreePlacement{Root: store, relative: filepath.Join("acme", filepath.Base(canonical))}, nil
		},
		worktreePath: func(placement WorktreePlacement, task, repository string) (string, error) {
			return filepath.Join(placement.Root, task, repository), nil
		},
		locateResumable: func(context.Context, *canonicalRepository, string, string, string, string) (string, error) {
			return "", nil
		},
		directoryExists:  func(string) (bool, error) { return false, nil },
		registeredBranch: func(context.Context, *canonicalRepository, string) (string, error) { return "feature", nil },
		activeClaim: func(string, string) (workLogClaim, workLogProjection, string, error) {
			return workLogClaim{}, workLogProjection{}, "", errors.New("unexpected claim read")
		},
		validateResume:   func(string, WorkLogOptions, workLogClaim) error { return nil },
		validateExisting: func(context.Context, *canonicalRepository, string, string) error { return nil },
		extendLog: func(_ string, requested WorkLogOptions, _ workLogClaim) (WorkLogOptions, error) {
			return requested, nil
		},
		synchronize: func(context.Context, *canonicalRepository, string, string) (string, error) { return revision, nil },
		git:         func(context.Context, *canonicalRepository, ...string) (string, error) { return revision, nil },
		configuredPlacement: func(_ context.Context, _ string, canonical *canonicalRepository, _ string) (worktreePlacement, error) {
			return worktreePlacement{Root: store, Relative: filepath.Join("acme", filepath.Base(canonical.path))}, nil
		},
		deriveBranch:   func(context.Context, branchNamingOptions) (string, error) { return "feature", nil },
		branchExists:   func(context.Context, *canonicalRepository, string) (bool, error) { return false, nil },
		branchWorktree: func(context.Context, *canonicalRepository, string) (bool, string, error) { return false, "", nil },
		prepareLocalRoot: func(context.Context, *canonicalRepository, string) (string, *os.File, error) {
			return store, &os.File{}, nil
		},
		prepareSharedRoot: func(root, task string) (preparedOperationRoot, error) {
			return preparedOperationRoot{Path: filepath.Join(root, task)}, nil
		},
		recoverLocalStage: func(context.Context, *canonicalRepository, string, string, string) (bool, error) { return false, nil },
		publish: func(context.Context, string, CreateOptions, preparedOperationRoot, *preparedOperationRoot, []createPlan, createPublicationPorts) ([]createAttempt, error) {
			return nil, nil
		},
		recordOwner: func(string, string, string, string, int) (OwnerRegistration, error) { return OwnerRegistration{}, nil },
	}
	return options
}

func TestCreateWorkflowUsesOnePlannedPublication(t *testing.T) {
	t.Parallel()
	options := fakeCreateOptions(t)
	called := false
	options.ports.publish = func(_ context.Context, _ string, _ CreateOptions, _ preparedOperationRoot, _ *preparedOperationRoot, plans []createPlan, _ createPublicationPorts) ([]createAttempt, error) {
		called = true
		if len(plans) != 1 || plans[0].result.Branch != "feature" || plans[0].baseRevision != strings.Repeat("a", 40) {
			t.Fatalf("planned publication = %+v", plans)
		}
		return nil, nil
	}
	results, err := Create(context.Background(), []string{"acme/app"}, options)
	if err != nil || !called || len(results) != 1 || results[0].Action != "created" {
		t.Fatalf("planned create results=%+v called=%t err=%v", results, called, err)
	}
}

func TestCreateWorkflowRefusesPreflightAndPlanningFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, want string }{
		{"invalid task", "one safe path segment"}, {"invalid repository", "must be owner/name"},
		{"Git capability", "Git unavailable"}, {"home resolution", "home unavailable"},
		{"store policy", "store unavailable"}, {"writable declaration", "declaration unavailable"},
		{"writable preflight", "writable unavailable"}, {"prompt snapshot", "prompt unavailable"},
		{"prompt reservation", "archive unavailable"}, {"operation preparation", "operation unavailable"},
		{"task lock", "lock unavailable"}, {"canonical missing", "canonical clone is missing"},
		{"contended task lock", "claim already held by a concurrent create"},
		{"canonical refusal", "canonical unavailable"}, {"user placement", "placement unavailable"},
		{"worktree path", "path unavailable"}, {"resume search", "resume search unavailable"},
		{"destination inspection", "destination unavailable"}, {"existing destination", "worktree already exists"},
		{"base synchronization", "synchronization unavailable"},
		{"configured placement", "configuration unavailable"}, {"placement drift", "worktree placement changed"},
		{"branch derivation", "branch derivation unavailable"}, {"branch lookup", "branch lookup unavailable"},
		{"branch exists", "already exists"}, {"branch occupancy lookup", "occupancy unavailable"},
		{"branch occupied", "already checked out"}, {"shared root", "shared root unavailable"},
		{"local root", "local root unavailable"}, {"local stage recovery", "local recovery unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := fakeCreateOptions(t)
			repositories := []string{"acme/app"}
			fault := errors.New(test.want)
			switch test.name {
			case "invalid task":
				options.Operation = ".."
			case "invalid repository":
				repositories = []string{"bad"}
			case "Git capability":
				options.ports.gitCapability = func() error { return fault }
			case "home resolution":
				options.ports.resolveHome = func(string) (wbhome.Resolution, error) { return wbhome.Resolution{}, fault }
			case "store policy":
				options.ports.storePolicy = func(string) (userStorePolicy, error) { return userStorePolicy{}, fault }
			case "writable declaration":
				options.ports.writableRequirements = func(string, string, []string, userStorePolicy, bool) ([]string, []pathguard.Requirement, error) {
					return nil, nil, fault
				}
			case "writable preflight":
				options.ports.checkWritable = func(string, []pathguard.Requirement, pathguard.Probe) error { return fault }
			case "prompt snapshot":
				options.ports.prepareLog = func(string, string, WorkLogOptions) (WorkLogOptions, error) { return WorkLogOptions{}, fault }
			case "prompt reservation":
				options.ports.reserveLog = func(string, string, WorkLogOptions) error { return fault }
			case "operation preparation":
				options.ports.prepareOperation = func(string, string, func()) (preparedOperationRoot, error) { return preparedOperationRoot{}, fault }
			case "task lock":
				options.ports.acquireLock = func(*os.File, string) (operationLock, error) { return operationLock{}, fault }
			case "contended task lock":
				options.ports.acquireLock = func(*os.File, string) (operationLock, error) { return operationLock{}, errOperationLockHeld }
			case "canonical missing":
				options.ports.openCanonical = func(string) (*canonicalRepository, error) { return nil, os.ErrNotExist }
			case "canonical refusal":
				options.ports.openCanonical = func(string) (*canonicalRepository, error) { return nil, fault }
			case "user placement":
				options.ports.userPlacement = func(context.Context, userStorePolicy, string, string) (WorktreePlacement, error) {
					return WorktreePlacement{}, fault
				}
			case "worktree path":
				options.ports.worktreePath = func(WorktreePlacement, string, string) (string, error) { return "", fault }
			case "resume search":
				options.ports.locateResumable = func(context.Context, *canonicalRepository, string, string, string, string) (string, error) {
					return "", fault
				}
			case "destination inspection":
				options.ports.directoryExists = func(string) (bool, error) { return false, fault }
			case "existing destination":
				options.ports.directoryExists = func(string) (bool, error) { return true, nil }
			case "base synchronization":
				options.ports.synchronize = func(context.Context, *canonicalRepository, string, string) (string, error) { return "", fault }
			case "configured placement":
				options.ports.configuredPlacement = func(context.Context, string, *canonicalRepository, string) (worktreePlacement, error) {
					return worktreePlacement{}, fault
				}
			case "placement drift":
				options.ports.configuredPlacement = func(context.Context, string, *canonicalRepository, string) (worktreePlacement, error) {
					return worktreePlacement{Root: filepath.Join(options.ProjectsRoot, "other"), Relative: "acme/app"}, nil
				}
			case "branch derivation":
				options.ports.deriveBranch = func(context.Context, branchNamingOptions) (string, error) { return "", fault }
			case "branch lookup":
				options.ports.branchExists = func(context.Context, *canonicalRepository, string) (bool, error) { return false, fault }
			case "branch exists":
				options.ports.branchExists = func(context.Context, *canonicalRepository, string) (bool, error) { return true, nil }
			case "branch occupancy lookup", "branch occupied":
				options.Resume = true
				options.ports.branchExists = func(context.Context, *canonicalRepository, string) (bool, error) { return true, nil }
				options.ports.branchWorktree = func(context.Context, *canonicalRepository, string) (bool, string, error) {
					if test.name == "branch occupancy lookup" {
						return false, "", fault
					}
					return true, "/occupied", nil
				}
			case "shared root":
				shared := filepath.Join(options.ProjectsRoot, "shared")
				options.ports.userPlacement = func(context.Context, userStorePolicy, string, string) (WorktreePlacement, error) {
					return WorktreePlacement{Root: shared, relative: "acme/app"}, nil
				}
				options.ports.configuredPlacement = func(context.Context, string, *canonicalRepository, string) (worktreePlacement, error) {
					return worktreePlacement{Root: shared, Relative: "acme/app"}, nil
				}
				options.ports.prepareSharedRoot = func(string, string) (preparedOperationRoot, error) { return preparedOperationRoot{}, fault }
			case "local root", "local stage recovery":
				localCreatePlacement(&options)
				if test.name == "local root" {
					options.ports.prepareLocalRoot = func(context.Context, *canonicalRepository, string) (string, *os.File, error) {
						return "", nil, fault
					}
				} else {
					options.Resume = true
					options.ports.recoverLocalStage = func(context.Context, *canonicalRepository, string, string, string) (bool, error) {
						return false, fault
					}
				}
			}
			_, err := Create(context.Background(), repositories, options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s refusal = %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func localCreatePlacement(options *CreateOptions) {
	options.ports.userPlacement = func(context.Context, userStorePolicy, string, string) (WorktreePlacement, error) {
		return WorktreePlacement{Root: options.ProjectsRoot, RepositoryLocal: true, relative: "acme/app"}, nil
	}
	options.ports.configuredPlacement = func(context.Context, string, *canonicalRepository, string) (worktreePlacement, error) {
		return worktreePlacement{Root: options.ProjectsRoot, Local: true, Relative: "acme/app"}, nil
	}
}

func fakeResumedCreateOptions(t *testing.T) CreateOptions {
	t.Helper()
	options := fakeCreateOptions(t)
	options.Resume = true
	options.ports.directoryExists = func(string) (bool, error) { return true, nil }
	options.ports.activeClaim = func(_, worktree string) (workLogClaim, workLogProjection, string, error) {
		return workLogClaim{Task: "task", Repository: "acme/app", Branch: "feature", Base: "main",
				BaseSHA: strings.Repeat("a", 40), EffortID: "task", RunID: "run", Model: "unknown"},
			workLogProjection{}, filepath.Join(worktree, "claim.json"), nil
	}
	return options
}

func TestCreateWorkflowResumesExistingAuthorityWithoutRepublishing(t *testing.T) {
	t.Parallel()
	options := fakeResumedCreateOptions(t)
	calledOwner := false
	options.ports.recordOwner = func(_, effort, _, _ string, _ int) (OwnerRegistration, error) {
		calledOwner = true
		if effort != "task" {
			t.Fatalf("resumed effort = %q", effort)
		}
		return OwnerRegistration{}, nil
	}
	results, err := Create(context.Background(), []string{"acme/app"}, options)
	if err != nil || len(results) != 1 || results[0].Action != "resumed" || !calledOwner || results[0].WorkLogPath == "" {
		t.Fatalf("resumed result=%+v owner=%t err=%v", results, calledOwner, err)
	}
}

func TestCreateWorkflowRefusesInconsistentResumeAuthority(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, want string }{
		{"branch lookup", "registered branch unavailable"},
		{"exact branch mismatch", "cannot resume"},
		{"claim identity", "claim identity does not match"},
		{"resume request", "resume request unavailable"},
		{"claim read", "recover active work-log claim"},
		{"existing checkout", "existing checkout unavailable"},
		{"owner record", "owner record unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := fakeResumedCreateOptions(t)
			fault := errors.New(test.want)
			switch test.name {
			case "branch lookup":
				options.ports.registeredBranch = func(context.Context, *canonicalRepository, string) (string, error) { return "", fault }
			case "exact branch mismatch":
				options.BranchChosen, options.Branch = true, "different"
			case "claim identity":
				options.ports.activeClaim = func(string, string) (workLogClaim, workLogProjection, string, error) {
					return workLogClaim{Task: "other"}, workLogProjection{}, "", nil
				}
			case "resume request":
				options.ports.validateResume = func(string, WorkLogOptions, workLogClaim) error { return fault }
			case "claim read":
				options.ports.activeClaim = func(string, string) (workLogClaim, workLogProjection, string, error) {
					return workLogClaim{}, workLogProjection{}, "", fault
				}
			case "existing checkout":
				options.ports.validateExisting = func(context.Context, *canonicalRepository, string, string) error { return fault }
			case "owner record":
				options.ports.recordOwner = func(string, string, string, string, int) (OwnerRegistration, error) {
					return OwnerRegistration{}, fault
				}
			}
			_, err := Create(context.Background(), []string{"acme/app"}, options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s refusal = %v, want %q", test.name, err, test.want)
			}
		})
	}
}

func TestCreateWorkflowRepairsLegacyClaimlessResumeAtExactMergeBase(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, want string }{
		{"success", ""}, {"merge base unavailable", "recover legacy worktree base"},
		{"invalid merge base", "recover legacy worktree base"},
		{"prompt snapshot unavailable", "prompt unavailable"},
		{"prompt archive unavailable", "archive unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := fakeResumedCreateOptions(t)
			options.ports.activeClaim = func(string, string) (workLogClaim, workLogProjection, string, error) {
				return workLogClaim{}, workLogProjection{}, "", errWorkLogProjectionNotFound
			}
			if test.name == "merge base unavailable" {
				options.ports.git = func(context.Context, *canonicalRepository, ...string) (string, error) {
					return "", errors.New("merge unavailable")
				}
			}
			if test.name == "invalid merge base" {
				options.ports.git = func(context.Context, *canonicalRepository, ...string) (string, error) { return "bad", nil }
			}
			if test.name == "prompt snapshot unavailable" {
				options.ports.prepareLog = func(string, string, WorkLogOptions) (WorkLogOptions, error) {
					return WorkLogOptions{}, errors.New("prompt unavailable")
				}
			}
			if test.name == "prompt archive unavailable" {
				options.ports.reserveLog = func(string, string, WorkLogOptions) error { return errors.New("archive unavailable") }
			}
			results, err := Create(context.Background(), []string{"acme/app"}, options)
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("legacy %s refusal = %v", test.name, err)
				}
			} else if err != nil || len(results) != 1 || results[0].BaseSHA != strings.Repeat("a", 40) || results[0].Action != "resumed" {
				t.Fatalf("legacy resume result=%+v err=%v", results, err)
			}
		})
	}
}

func TestCreateWorkflowCoordinatesExistingRunsAndNewRepositories(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, want   string
		repositories []string
	}{
		{"extends original run", "", []string{"acme/a", "acme/b"}},
		{"different existing runs", "different active Work Log runs", []string{"acme/a", "acme/b", "acme/c"}},
		{"extension unavailable", "extension unavailable", []string{"acme/a", "acme/b"}},
		{"extension archive unavailable", "archive unavailable", []string{"acme/a", "acme/b"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := fakeCreateOptions(t)
			options.Resume = true
			options.ports.directoryExists = func(path string) (bool, error) { return filepath.Base(path) != "b" && filepath.Base(path) != "c", nil }
			if test.name == "different existing runs" {
				options.ports.directoryExists = func(path string) (bool, error) { return filepath.Base(path) != "c", nil }
			}
			options.ports.activeClaim = func(_, path string) (workLogClaim, workLogProjection, string, error) {
				repository := "acme/" + filepath.Base(path)
				run := "run"
				if test.name == "different existing runs" && filepath.Base(path) == "b" {
					run = "other-run"
				}
				return workLogClaim{Task: "task", Repository: repository, Branch: "feature", Base: "main",
					BaseSHA: strings.Repeat("a", 40), EffortID: "task", RunID: run}, workLogProjection{}, "claim", nil
			}
			options.ports.extendLog = func(_ string, requested WorkLogOptions, _ workLogClaim) (WorkLogOptions, error) {
				if test.name == "extension unavailable" {
					return WorkLogOptions{}, errors.New("extension unavailable")
				}
				return requested, nil
			}
			if test.name == "extension archive unavailable" {
				options.ports.reserveLog = func(string, string, WorkLogOptions) error { return errors.New("archive unavailable") }
			}
			_, err := Create(context.Background(), test.repositories, options)
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("coordinated resume %s = %v", test.name, err)
				}
			} else if err != nil {
				t.Fatalf("coordinated resume = %v", err)
			}
		})
	}
}

func TestCreateWorkflowPreservesDiscoveredCheckoutPath(t *testing.T) {
	t.Parallel()
	options := fakeCreateOptions(t)
	want := filepath.Join(options.ProjectsRoot, "moved", "app")
	options.ports.locateResumable = func(context.Context, *canonicalRepository, string, string, string, string) (string, error) {
		return want, nil
	}
	results, err := Create(context.Background(), []string{"acme/app"}, options)
	if err != nil || len(results) != 1 || results[0].WorktreeDir != want {
		t.Fatalf("discovered checkout result=%+v err=%v", results, err)
	}
}

func TestCreateWorkflowReusesOneExternalSharedRoot(t *testing.T) {
	t.Parallel()
	options := fakeCreateOptions(t)
	shared := filepath.Join(options.ProjectsRoot, "shared")
	options.ports.userPlacement = func(context.Context, userStorePolicy, string, string) (WorktreePlacement, error) {
		return WorktreePlacement{Root: shared, relative: "acme/app"}, nil
	}
	options.ports.configuredPlacement = func(context.Context, string, *canonicalRepository, string) (worktreePlacement, error) {
		return worktreePlacement{Root: shared, Relative: "acme/app"}, nil
	}
	prepares := 0
	options.ports.prepareSharedRoot = func(root, task string) (preparedOperationRoot, error) {
		prepares++
		return preparedOperationRoot{Path: filepath.Join(root, task)}, nil
	}
	results, err := Create(context.Background(), []string{"acme/a", "acme/b"}, options)
	if err != nil || len(results) != 2 || prepares != 1 {
		t.Fatalf("shared publication results=%+v prepares=%d err=%v", results, prepares, err)
	}
}

func TestCreateWorkflowRecoversTaskBoundLocalStage(t *testing.T) {
	t.Parallel()
	options := fakeCreateOptions(t)
	options.Resume = true
	localCreatePlacement(&options)
	options.ports.recoverLocalStage = func(context.Context, *canonicalRepository, string, string, string) (bool, error) {
		return true, nil
	}
	called := false
	options.ports.publish = func(_ context.Context, _ string, _ CreateOptions, _ preparedOperationRoot, _ *preparedOperationRoot, plans []createPlan, _ createPublicationPorts) ([]createAttempt, error) {
		called = true
		if !plans[0].recoveredStage || plans[0].result.Action != "recovered" {
			t.Fatalf("recovered plan = %+v", plans[0])
		}
		return nil, nil
	}
	results, err := Create(context.Background(), []string{"acme/app"}, options)
	if err != nil || !called || len(results) != 1 || results[0].Action != "recovered" {
		t.Fatalf("recovered result=%+v called=%t err=%v", results, called, err)
	}
}

func TestCreateWorkflowCallsPostLockHookAndReturnsPublicationError(t *testing.T) {
	t.Parallel()
	options := fakeCreateOptions(t)
	called := false
	options.afterOperationRootPrepared = func() { called = true }
	fault := errors.New("publication unavailable")
	options.ports.publish = func(context.Context, string, CreateOptions, preparedOperationRoot, *preparedOperationRoot, []createPlan, createPublicationPorts) ([]createAttempt, error) {
		return nil, fault
	}
	_, err := Create(context.Background(), []string{"acme/app"}, options)
	if !called || !errors.Is(err, fault) {
		t.Fatalf("post-lock hook called=%t publication error=%v", called, err)
	}
}
