package depsrun

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/npmrelease"
)

// These failure-only ports exercise custody and ordering; provider observations
// are simulated here. Original integration cases retain the actual engines.
func TestPublicationPreparedStagesPreserveReceiptAndOutputCustody(t *testing.T) {
	t.Parallel()
	failure := errors.New("selected stage refusal")
	for _, stage := range []string{"plan", "plan bump", "plan output", "inspect", "bump load", "pre bump", "pre output", "provider persist", "provider output", "provider findings", "resume validation", "events", "final persist", "final output", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			request := testPublicationRequest(validNpmPublishOptions(), &invocation{projectsRoot: t.TempDir()})
			request.Apply = stage != "plan" && stage != "plan bump" && stage != "plan output"
			request.Lifecycle.Resume = stage == "bump load" || stage == "resume validation"
			releases, operation, err := npmPublicationIdentity(request)
			if err != nil {
				t.Fatal(err)
			}
			prepared := npmPublishPrepared{releases: releases, operation: operation, reportDir: "private-report"}
			var order []string
			writes := 0
			calls := 0
			ports := PublicationDependencies{
				Run: func(ctx context.Context, got []npmrelease.Release, options npmrelease.Options) (npmrelease.Report, error) {
					if ctx != t.Context() || !reflect.DeepEqual(got, releases) {
						t.Fatal("provider context/tuples changed")
					}
					if options.DryRun {
						order = append(order, "plan")
						if stage == "plan" {
							return npmrelease.Report{}, failure
						}
						return npmrelease.Report{Releases: []npmrelease.Receipt{{Release: releases[0]}}}, nil
					}
					order = append(order, "publish")
					if options.Persist == nil || options.Previous != nil && stage != "bump load" {
						t.Fatal("provider checkpoint contract")
					}
					report := npmrelease.Report{Status: npmrelease.StatusPublished, Releases: []npmrelease.Receipt{{Release: releases[0], Status: npmrelease.StatusPublished, RegistryVersion: releases[0].Version, RegistryCheckedAt: time.Now()}}}
					if stage == "events" {
						report.Status = npmrelease.StatusPlanned
					}
					if stage == "provider persist" || stage == "provider output" || stage == "provider findings" {
						return report, failure
					}
					if err := options.Persist(report); err != nil {
						return report, err
					}
					return report, nil
				},
				ReportExists: func(string) (bool, error) {
					order = append(order, "inspect")
					if stage == "inspect" {
						return false, failure
					}
					return request.Lifecycle.Resume, nil
				},
				LoadPublication: func(string) (npmrelease.Report, error) { return npmrelease.Report{}, nil },
				LoadBump: func(string) (deps.BumpReport, error) {
					if stage == "resume validation" {
						return deps.BumpReport{}, nil
					}
					return deps.BumpReport{}, failure
				},
				Bump: func(ctx context.Context, r BumpRequest) (BumpResult, error) {
					calls++
					order = append(order, "bump")
					if ctx != t.Context() || r.ProjectsRoot != request.Selection.ProjectsRoot || r.Options.NoRegistry != (calls == 1) {
						t.Fatal("bump custody changed")
					}
					r.Finish("completed")
					if stage == "plan bump" || stage == "pre bump" || stage == "pre output" {
						return BumpResult{Report: deps.BumpReport{Operation: "wave"}}, failure
					}
					return BumpResult{Report: deps.BumpReport{Operation: "wave"}}, nil
				},
				WritePublication: func(_ string, _ npmrelease.Report) error {
					order = append(order, "persist")
					writes++
					if stage == "provider persist" || stage == "final persist" && writes == 2 {
						return failure
					}
					return nil
				},
			}
			var code int
			service := NewPublication(ports, func(c int, _ string) error { code = c; order = append(order, "findings"); return failure })
			callbacks := PublicationCallbacks{StartProgress: func(string) PublicationProgress {
				return PublicationProgress{Finish: func(string) { order = append(order, "finish") }}
			}, Emit: func(out PublicationOutput) error {
				order = append(order, "emit")
				if out.Propagation == nil {
					t.Fatal("wave receipt omitted")
				}
				if stage == "plan output" || stage == "pre output" || stage == "provider output" || stage == "final output" {
					return failure
				}
				return nil
			}}
			err = service.runPrepared(t.Context(), request, prepared, callbacks)
			if stage == "success" {
				if err != nil || calls != 2 {
					t.Fatalf("success=%v bump calls=%d", err, calls)
				}
			} else if !errors.Is(err, failure) && stage != "events" && stage != "resume validation" {
				t.Fatalf("stage %s err=%v order=%v", stage, err, order)
			}
			if stage == "events" && err == nil {
				t.Fatal("missing registry evidence accepted")
			}
			if stage == "provider findings" {
				if code != 1 || !reflect.DeepEqual(order[len(order)-3:], []string{"persist", "emit", "findings"}) {
					t.Fatalf("durable/output/findings order=%v code=%d", order, code)
				}
			}
			if stage == "provider persist" && containsPublicationStage(order, "emit") {
				t.Fatalf("output preceded durable receipt: %v", order)
			}
		})
	}
}
func containsPublicationStage(stages []string, want string) bool {
	for _, stage := range stages {
		if stage == want {
			return true
		}
	}
	return false
}

func TestPublicationPreflightKeepsDetachedSelectionAndLateFormat(t *testing.T) {
	t.Parallel()
	request := testPublicationRequest(validNpmPublishOptions(), &invocation{projectsRoot: t.TempDir()})
	request.Lifecycle.ReportDir = t.TempDir()
	var stages []string
	ports := DefaultPublicationDependencies(nil)
	ports.Select = func(ctx context.Context, s Selection) ([]deps.Repository, error) {
		stages = append(stages, "select")
		if ctx != context.Background() || s.ProjectsRoot != request.Selection.ProjectsRoot {
			t.Fatal("selection context/root changed")
		}
		return nil, nil
	}
	service := NewPublication(ports, func(_ int, s string) error { return errors.New(s) })
	callbacks := testPublicationCallbacks()
	callbacks.ValidateOutput = func() error { stages = append(stages, "format"); return nil }
	if _, err := service.preflight(t.Context(), request, callbacks); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stages, []string{"format", "select"}) {
		t.Fatal(stages)
	}
	request.Selection.Fleet = false
	stages = nil
	if _, err := service.preflight(t.Context(), request, callbacks); err == nil || len(stages) != 0 {
		t.Fatalf("flag refusal ran format=%v err=%v", stages, err)
	}
}

func TestPublicationPreflightEffectRefusalsStayBeforeSelection(t *testing.T) {
	t.Parallel()
	failure := errors.New("private stage failure")
	for _, stage := range []string{"format", "home", "resume publication", "resume bump", "missing selector", "selection", "resume success", "resume validation"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			request := testPublicationRequest(validNpmPublishOptions(), &invocation{projectsRoot: t.TempDir()})
			request.Apply = stage == "resume publication" || stage == "resume bump" || stage == "resume success" || stage == "resume validation"
			request.Lifecycle.Resume = stage == "resume bump" || stage == "resume success" || stage == "resume validation"
			if stage == "resume validation" {
				request.Lifecycle.Resume = true
			}
			if stage == "resume success" {
				request.Lifecycle.Merge = true
			}
			ports := DefaultPublicationDependencies(nil)
			selected := false
			ports.Select = func(context.Context, Selection) ([]deps.Repository, error) {
				selected = true
				if stage == "selection" {
					return nil, failure
				}
				return nil, nil
			}
			ports.EnsureRoot = func(string) (string, error) {
				if stage == "home" {
					return "", failure
				}
				return t.TempDir(), nil
			}
			ports.ReportExists = func(string) (bool, error) {
				if stage == "resume publication" {
					return false, failure
				}
				return request.Lifecycle.Resume, nil
			}
			ports.LoadPublication = func(string) (npmrelease.Report, error) { return npmrelease.Report{}, nil }
			ports.LoadBump = func(string) (deps.BumpReport, error) {
				if stage == "resume bump" {
					return deps.BumpReport{}, failure
				}
				return deps.BumpReport{}, nil
			}
			if stage == "missing selector" {
				ports.Select = nil
			}
			cb := testPublicationCallbacks()
			cb.ValidateOutput = func() error {
				if stage == "format" {
					return failure
				}
				return nil
			}
			_, err := NewPublication(ports, func(_ int, s string) error { return errors.New(s) }).preflight(t.Context(), request, cb)
			if stage == "resume success" {
				if err != nil || !selected {
					t.Fatalf("resume=%v selected=%v", err, selected)
				}
				return
			}
			if stage == "resume validation" {
				if err == nil {
					t.Fatal("resumed wave in dry-run mode accepted")
				}
				return
			}
			if err == nil {
				t.Fatalf("%s accepted", stage)
			}
			if stage != "selection" && selected {
				t.Fatalf("%s discovered before refusal", stage)
			}
			if stage != "missing selector" && !errors.Is(err, failure) {
				t.Fatalf("identity=%v", err)
			}
		})
	}
}

func TestPublicationRunIdentityAndPartialClaimRefusalsPreserveLocks(t *testing.T) {
	t.Parallel()
	service := testPublicationService()
	if err := service.Run(t.Context(), PublicationRequest{}, testPublicationCallbacks()); err == nil {
		t.Fatal("empty tuple accepted")
	}
	request := testPublicationRequest(validNpmPublishOptions(), &invocation{projectsRoot: t.TempDir()})
	releases, operation, err := npmPublicationIdentity(request)
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := service.deps.AcquireLock(request.Selection.ProjectsRoot, operation, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Run(t.Context(), request, testPublicationCallbacks()); err == nil {
		t.Fatal("active campaign accepted")
	}
	if err := campaign.Release(); err != nil {
		t.Fatal(err)
	}
	claim, err := service.deps.AcquireLock(request.Selection.ProjectsRoot, npmrelease.PublicationClaimOperationIDs(releases)[0], false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Release() })
	if err := service.Run(t.Context(), request, testPublicationCallbacks()); err == nil {
		t.Fatal("active claim accepted")
	}
	campaign, err = service.deps.AcquireLock(request.Selection.ProjectsRoot, operation, false)
	if err != nil {
		t.Fatalf("partial claim leaked campaign=%v", err)
	}
	if err := campaign.Release(); err != nil {
		t.Fatal(err)
	}
}
