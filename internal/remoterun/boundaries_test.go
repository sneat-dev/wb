package remoterun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

var errObservedProvider = errors.New("observed provider failure")

type observedProvider struct {
	remotestate.Provider
	list    func(context.Context) ([]remotestate.Entry, error)
	claim   func(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error)
	release func(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error)
	claims  func(context.Context) ([]remotestate.ClaimEntry, error)
}

func (p *observedProvider) List(ctx context.Context) ([]remotestate.Entry, error) { return p.list(ctx) }
func (p *observedProvider) Claim(ctx context.Context, c remotestate.Claim, m remotestate.ClaimMode, h string) (remotestate.ClaimOutcome, error) {
	return p.claim(ctx, c, m, h)
}
func (p *observedProvider) Release(ctx context.Context, t, l, m string, f bool) (remotestate.ReleaseOutcome, error) {
	return p.release(ctx, t, l, m, f)
}
func (p *observedProvider) Claims(ctx context.Context) ([]remotestate.ClaimEntry, error) {
	return p.claims(ctx)
}

type observedExit struct {
	code    int
	message string
}

func (e *observedExit) Error() string             { return e.message }
func boundaryExit(code int, message string) error { return &observedExit{code, message} }
func providerService(t *testing.T, p remotestate.Provider) *Service {
	t.Helper()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("remote:\n  repo: team/state\n  machine: local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return New(Dependencies{ConfigPath: func() string { return config }, Login: func() (string, error) { return "me", nil }, Open: func(remotestate.Config, string) (remotestate.Provider, error) { return p, nil }, Now: func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }, ExitError: boundaryExit})
}
func expectObservedExit(t *testing.T, err error, code int, fragment string) {
	t.Helper()
	var actual *observedExit
	if !errors.As(err, &actual) || actual.code != code || !strings.Contains(actual.message, fragment) {
		t.Fatalf("coded refusal: %v", err)
	}
}

func TestProviderObservationFailuresPreserveCodedErrorsAndDetachedContext(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"machines", "claims", "claims-list", "status", "claim-login", "release-login", "claim", "release"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			check := func(ctx context.Context) {
				if ctx != context.Background() {
					t.Fatal("selection/claims context no longer detached")
				}
			}
			p := &observedProvider{
				list: func(ctx context.Context) ([]remotestate.Entry, error) { check(ctx); return nil, errObservedProvider },
				claims: func(ctx context.Context) ([]remotestate.ClaimEntry, error) {
					check(ctx)
					if stage == "claims-list" {
						return nil, nil
					}
					return nil, errObservedProvider
				},
				claim: func(ctx context.Context, _ remotestate.Claim, _ remotestate.ClaimMode, _ string) (remotestate.ClaimOutcome, error) {
					check(ctx)
					return remotestate.ClaimOutcome{}, errObservedProvider
				},
				release: func(ctx context.Context, _, _, _ string, _ bool) (remotestate.ReleaseOutcome, error) {
					check(ctx)
					return remotestate.ReleaseOutcome{}, errObservedProvider
				},
			}
			s := providerService(t, p)
			var err error
			code := 1
			switch stage {
			case "machines":
				_, err = s.Machines("explicit", time.Hour)
			case "claims", "claims-list":
				_, err = s.Claims("explicit", time.Hour)
			case "status":
				var progress []string
				_, err = s.Status(StatusRequest{ProjectsRoot: "explicit"}, StatusProgress{Start: func(v string) { progress = append(progress, v) }, Finish: func(v string) { progress = append(progress, v) }})
				if len(progress) != 2 || !strings.Contains(progress[1], "failed") {
					t.Fatal(progress)
				}
			case "claim-login":
				s.deps.Login = func() (string, error) { return "", errObservedProvider }
				_, err = s.Claim(ClaimRequest{ProjectsRoot: "explicit", Task: "task"})
				code = 2
			case "release-login":
				s.deps.Login = func() (string, error) { return "", nil }
				_, err = s.Release(ReleaseRequest{ProjectsRoot: "explicit", Task: "task"})
				code = 2
			case "claim":
				_, err = s.Claim(ClaimRequest{ProjectsRoot: "explicit", Task: "task"})
			case "release":
				_, err = s.Release(ReleaseRequest{ProjectsRoot: "explicit", Task: "task"})
			}
			fragment := errObservedProvider.Error()
			if strings.HasSuffix(stage, "login") {
				fragment = "GitHub login"
			}
			expectObservedExit(t, err, code, fragment)
		})
	}
}

func TestConditionalTakeoverObservationsKeepActualHolderAndPrevious(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"list", "takeover-error", "race", "previous", "force"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			original := remotestate.Claim{Login: "old", Machine: "box", Task: "task"}
			replacement := remotestate.Claim{Login: "new", Machine: "box", Task: "task"}
			var modes []remotestate.ClaimMode
			p := &observedProvider{
				list: func(context.Context) ([]remotestate.Entry, error) {
					if stage == "list" {
						return nil, errObservedProvider
					}
					return []remotestate.Entry{{Error: "bad snapshot"}}, nil
				},
				claim: func(_ context.Context, _ remotestate.Claim, mode remotestate.ClaimMode, expected string) (remotestate.ClaimOutcome, error) {
					modes = append(modes, mode)
					if mode == remotestate.ClaimNormal {
						return remotestate.ClaimOutcome{Kind: remotestate.ClaimHeld, Current: original}, nil
					}
					if mode == remotestate.ClaimTakeOverStale && expected != original.Holder() {
						t.Fatalf("expected holder=%q", expected)
					}
					if stage == "force" || stage == "takeover-error" {
						return remotestate.ClaimOutcome{}, errObservedProvider
					}
					if stage == "race" {
						return remotestate.ClaimOutcome{Kind: remotestate.ClaimHeld, Current: replacement}, nil
					}
					return remotestate.ClaimOutcome{Kind: remotestate.ClaimAcquired, Previous: &replacement}, nil
				},
			}
			s := providerService(t, p)
			result, err := s.Claim(ClaimRequest{ProjectsRoot: "explicit", Task: "task", TakeOver: true, Force: stage == "force", Stale: time.Hour})
			if stage == "previous" {
				if err != nil || !strings.Contains(result.Text, "new/box") {
					t.Fatalf("previous custody: %+v %v", result, err)
				}
			} else {
				fragment := errObservedProvider.Error()
				if stage == "race" {
					fragment = "changed to new/box"
				}
				expectObservedExit(t, err, 1, fragment)
			}
			var advisory bytes.Buffer
			auto := s.AutoClaim("explicit", "task", time.Hour, &advisory)
			if stage == "previous" {
				if auto.Outcome != "took_over" || !strings.Contains(auto.Detail, "new/box") {
					t.Fatalf("auto previous: %+v", auto)
				}
			} else if stage == "race" {
				if auto.Outcome != "held" || !strings.Contains(auto.Detail, "new/box") {
					t.Fatalf("auto race: %+v", auto)
				}
			} else if auto.Outcome != "skipped" {
				t.Fatalf("auto error: %+v", auto)
			}
			for _, m := range modes {
				if m == remotestate.ClaimForce && stage != "force" {
					t.Fatal("unexpected force")
				}
			}
		})
	}
}

func TestAutoAdvisoriesHandleEmptyLoginRefreshAndReleaseFailures(t *testing.T) {
	t.Parallel()
	p := &observedProvider{claim: func(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
		return remotestate.ClaimOutcome{Kind: remotestate.ClaimRefreshed}, nil
	}, release: func(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
		return remotestate.ReleaseOutcome{}, errObservedProvider
	}}
	s := providerService(t, p)
	var out bytes.Buffer
	if got := s.AutoClaim("explicit", "task", time.Hour, &out); got.Outcome != "refreshed" || !strings.Contains(out.String(), "refreshed") {
		t.Fatalf("refresh=%+v output=%q", got, out.String())
	}
	s.deps.Login = func() (string, error) { return "", nil }
	if got := s.AutoClaim("explicit", "task", time.Hour, &out); got.Outcome != "skipped" || !strings.Contains(got.Detail, "empty login") {
		t.Fatal(got)
	}
	if got := s.AutoRelease("explicit", "task", &out); got.Outcome != "skipped" || !strings.Contains(got.Detail, "empty login") {
		t.Fatal(got)
	}
	s.deps.Login = func() (string, error) { return "", errObservedProvider }
	if got := s.AutoRelease("explicit", "task", &out); got.Outcome != "skipped" || !strings.Contains(got.Detail, errObservedProvider.Error()) {
		t.Fatal(got)
	}
	s.deps.Login = func() (string, error) { return "me", nil }
	if got := s.AutoRelease("explicit", "task", &out); !got.Leaked() || !strings.Contains(got.Detail, errObservedProvider.Error()) {
		t.Fatal(got)
	}
	p.release = func(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
		return remotestate.ReleaseOutcome{Kind: remotestate.ReleaseHeldByOther}, nil
	}
	if got := s.AutoRelease("explicit", "task", &out); got.Outcome != "skipped" || got.Detail != "held by another machine" {
		t.Fatal(got)
	}
	p.release = func(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
		return remotestate.ReleaseOutcome{Kind: remotestate.ReleaseHeldByOther, Current: &remotestate.Claim{Login: "other", Machine: "box"}}, nil
	}
	if got := s.AutoRelease("explicit", "task", &out); got.Outcome != "skipped" || !strings.Contains(got.Detail, "other/box") {
		t.Fatal(got)
	}
}

func TestReadLoadFailuresReturnBeforeProviderEffects(t *testing.T) {
	t.Parallel()
	s := New(Dependencies{ConfigPath: func() string { return filepath.Join(t.TempDir(), "missing") }, ExitError: boundaryExit})
	for _, operation := range []func() error{
		func() error { _, err := s.Machines("explicit", time.Hour); return err },
		func() error { _, err := s.Status(StatusRequest{}, StatusProgress{}); return err },
		func() error { _, _, err := s.Load("explicit"); return err },
	} {
		if err := operation(); err == nil {
			t.Fatal("unconfigured service accepted")
		}
	}
}

func TestDefaultRemoteOperationsUseRealOpenAndClock(t *testing.T) {
	t.Parallel()
	deps := DefaultDependencies(boundaryExit)
	if now := deps.Now(); now.Location() != time.UTC || time.Since(now) > time.Second {
		t.Fatalf("clock=%v", now)
	}
	// Invalid hub configuration fails in the genuine native provider constructor,
	// before any network call. This is not a simulated successful provider.
	if _, err := deps.Open(remotestate.Config{Provider: "hub", URL: "invalid", Machine: "box"}, t.TempDir()); err == nil {
		t.Fatal("default provider accepted invalid origin")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := DefaultEnrollDependencies().Verify(ctx, "https://example.invalid", "box", "credential"); err == nil {
		t.Fatal("canceled real hub observation accepted")
	}
}

func TestEnrollmentReadAndConfigRollbackBoundaries(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"read", "config-created", "config-existing"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			config := filepath.Join(root, "config")
			token := filepath.Join(root, "credential")
			if err := os.WriteFile(config, []byte("not: [valid"), 0600); err != nil {
				t.Fatal(err)
			}
			if stage == "config-existing" {
				if err := os.WriteFile(token, []byte("credential"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			verify := 0
			s := NewEnroll(EnrollDependencies{ConfigPath: func() string { return config }, Verify: func(context.Context, string, string, string) error { verify++; return nil }}, boundaryExit)
			var input io.Reader = strings.NewReader("credential")
			if stage == "read" {
				input = observedErrorReader{}
			}
			_, err := s.Enroll(context.Background(), EnrollRequest{ProjectsRoot: root, Machine: "box", HubURL: "https://example.invalid", TokenFile: token, TokenStdin: true, Input: input})
			if err == nil {
				t.Fatal("expected actual read/config refusal")
			}
			if stage == "read" {
				expectObservedExit(t, err, 2, "read credential")
				if verify != 0 {
					t.Fatal("verified failed read")
				}
				return
			}
			if verify != 1 {
				t.Fatalf("verify=%d", verify)
			}
			data, readErr := os.ReadFile(token)
			if stage == "config-created" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("new token not rolled back: %v", readErr)
				}
			} else if readErr != nil || string(data) != "credential" {
				t.Fatalf("existing token custody=%q %v", data, readErr)
			}
		})
	}
}

type observedErrorReader struct{}

func (observedErrorReader) Read([]byte) (int, error) { return 0, fmt.Errorf("read credential failure") }
