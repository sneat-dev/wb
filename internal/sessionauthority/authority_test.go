package sessionauthority

import (
	"path/filepath"
	"strings"
	"testing"
)

// sdCovDigest builds an admitted-looking sha256 digest for happy-path fixtures.
func sdCovDigest(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

// sdCovObjectID builds a full lowercase Git object ID (SHA-1 length).
func sdCovObjectID(fill string) string { return strings.Repeat(fill, 40) }

// sdCovAbsoluteContinuation returns a clean absolute continuation path that is
// portable across the supported platforms.
func sdCovAbsoluteContinuation() string {
	return filepath.Join(string(filepath.Separator), "var", "lib", "wb", "continuation.json")
}

// sdCovTrackedLaunch is the minimal admitted pinned-clean launch that must
// validate: every field below is required authority.
func sdCovTrackedLaunch() Launch {
	return Launch{
		AggregateID:            "agg-0001",
		AggregateDigest:        sdCovDigest("a"),
		AggregateFile:          "aggregate.json",
		SuccessorWBSessionID:   "wbs-successor",
		PredecessorWBSessionID: "wbs-predecessor",
		TargetMachine:          "hetzner-vm1",
		SourceRuntime:          "codex",
		SourceModel:            "gpt-5",
		RequestedHarness:       "codex",
		RequestedModel:         "gpt-5-codex",
		PinnedCommit:           sdCovObjectID("b"),
		PinnedBranch:           "feature/session",
		ContinuationKind:       ContinuationTracked,
		ContinuationPath:       ".wb/handoffs/handoff-123.md",
		ContinuationDigest:     sdCovDigest("c"),
		RootMode:               LaunchRootPinnedClean,
	}
}

// sdCovPrivateLaunch is the minimal admitted parked-neutral launch that must
// validate: private continuation authority with no Git pin.
func sdCovPrivateLaunch() Launch {
	launch := sdCovTrackedLaunch()
	launch.PinnedCommit = ""
	launch.PinnedBranch = ""
	launch.ContinuationKind = ContinuationPrivate
	launch.ContinuationPath = sdCovAbsoluteContinuation()
	launch.RootMode = LaunchRootParkedNeutral
	return launch
}

func TestSdCovValidIDAcceptsOnlyOneFixedSafeID(t *testing.T) {
	valid := []string{"a", "A0", "agg-0001", "wbs.successor_1", "a" + strings.Repeat("b", 200)}
	for _, value := range valid {
		if !ValidID(value) {
			t.Errorf("ValidID(%q) = false, want true", value)
		}
	}
	invalid := []string{"", ".", "..", "_leading", "-leading", ".hidden", "a b", "a/b", `a\b`, "a\nb", "a:b"}
	for _, value := range invalid {
		if ValidID(value) {
			t.Errorf("ValidID(%q) = true, want false", value)
		}
	}
}

func TestSdCovValidateMemberKey(t *testing.T) {
	if err := ValidateMemberKey("member-1"); err != nil {
		t.Fatalf("ValidateMemberKey(member-1) = %v, want nil", err)
	}
	err := ValidateMemberKey("../escape")
	if err == nil {
		t.Fatal("ValidateMemberKey(../escape) = nil, want error")
	}
	if want := "member key must be one fixed safe ID"; !strings.Contains(err.Error(), want) {
		t.Fatalf("ValidateMemberKey error = %q, want it to contain %q", err, want)
	}
	if err := ValidateMemberKey(".."); err == nil {
		t.Fatal("ValidateMemberKey(..) = nil, want error")
	}
}

func TestSdCovLaunchValidateAcceptsAdmittedAuthority(t *testing.T) {
	if err := sdCovTrackedLaunch().Validate(); err != nil {
		t.Fatalf("tracked pinned-clean launch rejected: %v", err)
	}
	if err := sdCovPrivateLaunch().Validate(); err != nil {
		t.Fatalf("parked-neutral private launch rejected: %v", err)
	}
	// Empty RootMode is legacy read compatibility for pinned-clean launches.
	legacy := sdCovTrackedLaunch()
	legacy.RootMode = ""
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy empty root mode rejected: %v", err)
	}
	// Parked-local retains a Git pin plus private continuation authority.
	parkedLocal := sdCovTrackedLaunch()
	parkedLocal.RootMode = LaunchRootParkedLocal
	parkedLocal.ContinuationKind = ContinuationPrivate
	parkedLocal.ContinuationPath = sdCovAbsoluteContinuation()
	if err := parkedLocal.Validate(); err != nil {
		t.Fatalf("parked-local private launch rejected: %v", err)
	}
}

func TestSdCovLaunchValidateRejectsUnadmittedAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Launch)
		wantErr string
	}{
		{
			name:    "aggregate id",
			mutate:  func(l *Launch) { l.AggregateID = "../agg" },
			wantErr: "aggregate_id is not one fixed safe ID",
		},
		{
			name:    "successor session id",
			mutate:  func(l *Launch) { l.SuccessorWBSessionID = "" },
			wantErr: "successor_wb_session_id is not one fixed safe ID",
		},
		{
			name:    "predecessor session id",
			mutate:  func(l *Launch) { l.PredecessorWBSessionID = ".." },
			wantErr: "predecessor_wb_session_id is not one fixed safe ID",
		},
		{
			name:    "target machine",
			mutate:  func(l *Launch) { l.TargetMachine = "machine one" },
			wantErr: "target_machine is not one fixed safe ID",
		},
		{
			name:    "aggregate digest",
			mutate:  func(l *Launch) { l.AggregateDigest = "sha256:" + strings.Repeat("A", 64) },
			wantErr: "aggregate digest must be sha256:<64 lowercase hex characters>",
		},
		{
			name:    "continuation digest",
			mutate:  func(l *Launch) { l.ContinuationDigest = "sha256:short" },
			wantErr: "continuation digest must be sha256:<64 lowercase hex characters>",
		},
		{
			name:    "aggregate file",
			mutate:  func(l *Launch) { l.AggregateFile = "nested/aggregate.json" },
			wantErr: "aggregate file must be one fixed safe basename",
		},
		{
			name:    "aggregate file empty",
			mutate:  func(l *Launch) { l.AggregateFile = "" },
			wantErr: "aggregate file must be one fixed safe basename",
		},
		{
			name:    "source runtime missing",
			mutate:  func(l *Launch) { l.SourceRuntime = "   " },
			wantErr: "source runtime is required and must be single-line",
		},
		{
			name:    "source runtime multiline",
			mutate:  func(l *Launch) { l.SourceRuntime = "codex\nclaude" },
			wantErr: "source runtime is required and must be single-line",
		},
		{
			name:    "source model multiline",
			mutate:  func(l *Launch) { l.SourceModel = "gpt-5\r\nclaude" },
			wantErr: "model and requested harness must be single-line",
		},
		{
			name:    "requested harness multiline",
			mutate:  func(l *Launch) { l.RequestedHarness = "codex\n" },
			wantErr: "model and requested harness must be single-line",
		},
		{
			name:    "requested model multiline",
			mutate:  func(l *Launch) { l.RequestedModel = "gpt-5\nclaude" },
			wantErr: "model and requested harness must be single-line",
		},
		{
			name:    "pinned commit not an object id",
			mutate:  func(l *Launch) { l.PinnedCommit = strings.Repeat("b", 39) },
			wantErr: "pinned commit must be one full lowercase Git object ID",
		},
		{
			name:    "pinned commit uppercase",
			mutate:  func(l *Launch) { l.PinnedCommit = strings.Repeat("B", 40) },
			wantErr: "pinned commit must be one full lowercase Git object ID",
		},
		{
			name:    "pinned branch missing",
			mutate:  func(l *Launch) { l.PinnedBranch = " " },
			wantErr: "pinned branch is required and must be single-line",
		},
		{
			name:    "pinned branch multiline",
			mutate:  func(l *Launch) { l.PinnedBranch = "main\nother" },
			wantErr: "pinned branch is required and must be single-line",
		},
		{
			name: "parked local without private continuation",
			mutate: func(l *Launch) {
				l.RootMode = LaunchRootParkedLocal
				l.ContinuationKind = ContinuationTracked
			},
			wantErr: "parked-local launch requires private continuation authority",
		},
		{
			name:    "unsupported root mode",
			mutate:  func(l *Launch) { l.RootMode = "somewhere_else" },
			wantErr: `launch root mode "somewhere_else" is unsupported`,
		},
		{
			name: "tracked continuation path is the repository root",
			mutate: func(l *Launch) {
				l.ContinuationKind = ContinuationTracked
				l.ContinuationPath = "."
			},
			wantErr: "tracked continuation path must be repository-relative",
		},
		{
			name: "tracked continuation path is empty",
			mutate: func(l *Launch) {
				l.ContinuationKind = ContinuationTracked
				l.ContinuationPath = ""
			},
			wantErr: "tracked continuation path must be repository-relative",
		},
		{
			name: "tracked continuation path is absolute",
			mutate: func(l *Launch) {
				l.ContinuationKind = ContinuationTracked
				l.ContinuationPath = sdCovAbsoluteContinuation()
			},
			wantErr: "tracked continuation path must be repository-relative",
		},
		{
			name: "tracked continuation path escapes the parent",
			mutate: func(l *Launch) {
				l.ContinuationKind = ContinuationTracked
				l.ContinuationPath = "../outside.md"
			},
			wantErr: "tracked continuation path must be repository-relative",
		},
		{
			name: "tracked continuation path is the parent",
			mutate: func(l *Launch) {
				l.ContinuationKind = ContinuationTracked
				l.ContinuationPath = ".."
			},
			wantErr: "tracked continuation path must be repository-relative",
		},
		{
			name: "private continuation path is relative",
			mutate: func(l *Launch) {
				l.ContinuationKind = ContinuationPrivate
				l.ContinuationPath = "relative/continuation.json"
			},
			wantErr: "private continuation path must be clean and absolute",
		},
		{
			name: "private continuation path is not clean",
			mutate: func(l *Launch) {
				sep := string(filepath.Separator)
				l.ContinuationKind = ContinuationPrivate
				l.ContinuationPath = sep + "var" + sep + ".." + sep + "etc" + sep + "continuation.json"
			},
			wantErr: "private continuation path must be clean and absolute",
		},
		{
			name:    "unsupported continuation kind",
			mutate:  func(l *Launch) { l.ContinuationKind = "ephemeral" },
			wantErr: `continuation kind "ephemeral" is unsupported`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launch := sdCovTrackedLaunch()
			tc.mutate(&launch)
			err := launch.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestSdCovLaunchValidateParkedNeutralAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Launch)
		wantErr string
	}{
		{
			name:    "pinned commit present",
			mutate:  func(l *Launch) { l.PinnedCommit = sdCovObjectID("d") },
			wantErr: "parked-neutral launch requires private continuation authority without a Git pin",
		},
		{
			name:    "pinned branch present",
			mutate:  func(l *Launch) { l.PinnedBranch = "main" },
			wantErr: "parked-neutral launch requires private continuation authority without a Git pin",
		},
		{
			name:    "tracked continuation",
			mutate:  func(l *Launch) { l.ContinuationKind = ContinuationTracked },
			wantErr: "parked-neutral launch requires private continuation authority without a Git pin",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launch := sdCovPrivateLaunch()
			tc.mutate(&launch)
			err := launch.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
