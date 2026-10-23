package worktrees

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPublishCreatePlansKeepsPhysicalAndWorkLogPhasesOrdered(t *testing.T) {
	t.Parallel()
	var calls []string
	plan := createPlan{result: CreateResult{Repository: "acme/app", Branch: "feature", Base: "main"},
		placement: worktreePlacement{Relative: "acme/app"}, baseRevision: strings.Repeat("a", 40)}
	options := CreateOptions{Operation: "task", afterWorkLogClaim: func(CreateResult) error {
		calls = append(calls, "claim")
		return nil
	}, afterWorkLogProjection: func(CreateResult) error {
		calls = append(calls, "projection")
		return nil
	}}
	ports := createPublicationPorts{
		addWorktree: func(_ context.Context, request securePublicationRequest) error {
			calls = append(calls, "checkout")
			if request.repository != "app" || request.parent != "acme" || request.branch != "feature" || request.baseRevision != plan.baseRevision {
				t.Fatalf("physical publication request = %+v", request)
			}
			*request.publication = &createdWorktreePublication{}
			return nil
		},
		recordWorkLog: func(_, task string, result CreateResult, _ WorkLogOptions, hooks workLogPublicationHooks) (WorkLogPublicationOutcome, error) {
			calls = append(calls, "work-log")
			if task != "task" || result.Repository != "acme/app" || hooks.afterClaim == nil {
				t.Fatalf("work log arguments = %q %+v %+v", task, result, hooks)
			}
			if err := hooks.afterClaim(); err != nil {
				t.Fatal(err)
			}
			if err := hooks.afterProjection(); err != nil {
				t.Fatal(err)
			}
			return WorkLogPublicationOutcome{ClaimPath: "claim.json"}, nil
		},
	}
	attempts, err := publishCreatePlans(context.Background(), "home", options,
		preparedOperationRoot{Path: "operation"}, nil, []createPlan{plan}, ports)
	if err != nil || len(attempts) != 1 || attempts[0].plan.result.WorkLogPath != "claim.json" ||
		strings.Join(calls, ",") != "checkout,work-log,claim,projection" {
		t.Fatalf("attempts=%+v calls=%v err=%v", attempts, calls, err)
	}
}

func TestPublishCreatePlansUsesPreparedSharedOperation(t *testing.T) {
	t.Parallel()
	shared := preparedOperationRoot{Path: "shared"}
	plan := createPlan{result: CreateResult{Repository: "acme/app"}, placement: worktreePlacement{Relative: "acme/app"}}
	ports := createPublicationPorts{
		addWorktree: func(_ context.Context, request securePublicationRequest) error {
			if request.operationRoot != shared.Path {
				t.Fatalf("publication root = %q, want %q", request.operationRoot, shared.Path)
			}
			return nil
		},
		recordWorkLog: func(string, string, CreateResult, WorkLogOptions, workLogPublicationHooks) (WorkLogPublicationOutcome, error) {
			return WorkLogPublicationOutcome{}, nil
		},
	}
	if _, err := publishCreatePlans(context.Background(), "home", CreateOptions{}, preparedOperationRoot{Path: "default"}, &shared, []createPlan{plan}, ports); err != nil {
		t.Fatal(err)
	}
}

func TestPublishCreatePlansUsesPreparedLocalRoot(t *testing.T) {
	t.Parallel()
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	plan := createPlan{result: CreateResult{Repository: "acme/app"}, placement: worktreePlacement{Local: true}, localRoot: "local", localRootDir: directory}
	ports := createPublicationPorts{
		addWorktree: func(_ context.Context, request securePublicationRequest) error {
			if request.operationRoot != "local" || request.operationDirectory != directory || request.repository != "task" || request.parent != "" {
				t.Fatalf("local publication request = %+v", request)
			}
			return nil
		},
		recordWorkLog: func(string, string, CreateResult, WorkLogOptions, workLogPublicationHooks) (WorkLogPublicationOutcome, error) {
			return WorkLogPublicationOutcome{}, nil
		},
	}
	if _, err := publishCreatePlans(context.Background(), "home", CreateOptions{Operation: "task"}, preparedOperationRoot{}, nil, []createPlan{plan}, ports); err != nil {
		t.Fatal(err)
	}
}

func TestPublishCreatePlansCompensatesRecordedAttempts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name               string
		failCheckoutAt     int
		failWorkLogAt      int
		wantAttempts       int
		wantRecoveryAction string
	}{
		{name: "first checkout failure returns original error", failCheckoutAt: 1, wantAttempts: 0},
		{name: "second checkout failure compensates first", failCheckoutAt: 2, wantAttempts: 1, wantRecoveryAction: "create worktree"},
		{name: "first Work Log failure compensates its checkout", failWorkLogAt: 1, wantAttempts: 1, wantRecoveryAction: "record work log"},
		{name: "second Work Log failure compensates both", failWorkLogAt: 2, wantAttempts: 2, wantRecoveryAction: "record work log"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plans := []createPlan{
				{result: CreateResult{Repository: "acme/a"}, placement: worktreePlacement{Relative: "acme/a"}},
				{result: CreateResult{Repository: "acme/b"}, placement: worktreePlacement{Relative: "acme/b"}},
			}
			var checkoutCalls, workLogCalls, recoveryCalls int
			ports := createPublicationPorts{
				addWorktree: func(_ context.Context, request securePublicationRequest) error {
					checkoutCalls++
					if checkoutCalls == test.failCheckoutAt {
						return errors.New("checkout failed")
					}
					*request.publication = &createdWorktreePublication{}
					return nil
				},
				recordWorkLog: func(_, _ string, _ CreateResult, _ WorkLogOptions, _ workLogPublicationHooks) (WorkLogPublicationOutcome, error) {
					workLogCalls++
					if workLogCalls == test.failWorkLogAt {
						return WorkLogPublicationOutcome{ClaimPath: "partial"}, errors.New("claim failed")
					}
					return WorkLogPublicationOutcome{ClaimPath: "complete"}, nil
				},
				recover: func(_ context.Context, _ string, _ CreateOptions, attempts []createAttempt, _ error) ([]CreateRecoveryOutcome, error) {
					recoveryCalls++
					if len(attempts) != test.wantAttempts {
						t.Fatalf("recovery attempts = %d, want %d", len(attempts), test.wantAttempts)
					}
					return []CreateRecoveryOutcome{{Result: CreateResult{Repository: "acme/a"}}}, errors.New("recovery failed")
				},
			}
			attempts, err := publishCreatePlans(context.Background(), "home", CreateOptions{Operation: "task"},
				preparedOperationRoot{Path: "operation"}, nil, plans, ports)
			if len(attempts) != test.wantAttempts || err == nil {
				t.Fatalf("attempts=%d err=%v", len(attempts), err)
			}
			if test.wantRecoveryAction == "" {
				if recoveryCalls != 0 || !strings.Contains(err.Error(), "checkout failed") {
					t.Fatalf("first checkout error = %v; recovery calls=%d", err, recoveryCalls)
				}
				return
			}
			var publicationErr *CreatePublicationError
			if !errors.As(err, &publicationErr) || recoveryCalls != 1 || len(publicationErr.Outcomes) != 1 ||
				!strings.Contains(err.Error(), test.wantRecoveryAction) || !strings.Contains(err.Error(), "coordinated publication recovery") {
				t.Fatalf("publication error = %v; recovery calls=%d", err, recoveryCalls)
			}
		})
	}
}

func TestPublishCreatePlansResumedAndLocalPlacementBoundaries(t *testing.T) {
	t.Parallel()
	resumed := createPlan{resumed: true, resumeClaim: &workLogClaim{}}
	var called bool
	ports := createPublicationPorts{recordWorkLog: func(_, _ string, _ CreateResult, _ WorkLogOptions, _ workLogPublicationHooks) (WorkLogPublicationOutcome, error) {
		called = true
		return WorkLogPublicationOutcome{}, nil
	}}
	attempts, err := publishCreatePlans(context.Background(), "home", CreateOptions{}, preparedOperationRoot{}, nil, []createPlan{resumed}, ports)
	if err != nil || len(attempts) != 0 || called {
		t.Fatalf("existing claim was republished: attempts=%d called=%t err=%v", len(attempts), called, err)
	}
	missingLocal := createPlan{result: CreateResult{Repository: "acme/app"}, placement: worktreePlacement{Local: true}}
	attempts, err = publishCreatePlans(context.Background(), "home", CreateOptions{}, preparedOperationRoot{}, nil, []createPlan{missingLocal}, ports)
	if err == nil || !strings.Contains(err.Error(), "local worktree root was not prepared") || len(attempts) != 0 {
		t.Fatalf("missing local root = attempts=%d err=%v", len(attempts), err)
	}
}
