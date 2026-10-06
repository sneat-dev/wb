package orchestrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestRecordedMergeReceiptRecoveryRetainsCallerIntent(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name                        string
		recorded, current, deferred WorktreeMergeRoute
		link                        string
		local                       bool
		wantRoute                   WorktreeMergeRoute
		wantLink                    string
	}{
		{"empty route adopts deferral", "", "", WorktreeMergeRouteDirect, "", false, WorktreeMergeRouteDirect, "recorded-ci"},
		{"auto route adopts deferral", "", WorktreeMergeRouteAuto, WorktreeMergeRouteDirect, "", false, WorktreeMergeRouteDirect, "recorded-ci"},
		{"explicit PR route retains route", "", WorktreeMergeRoutePullRequest, WorktreeMergeRouteDirect, "", false, WorktreeMergeRoutePullRequest, "recorded-ci"},
		{"recorded PR retains route", WorktreeMergeRoutePullRequest, WorktreeMergeRouteAuto, WorktreeMergeRouteDirect, "", false, WorktreeMergeRoutePullRequest, "recorded-ci"},
		{"explicit link retains caller", "", WorktreeMergeRouteAuto, WorktreeMergeRouteDirect, "caller-ci", false, WorktreeMergeRouteAuto, "caller-ci"},
		{"local validation retains caller", "", WorktreeMergeRouteAuto, WorktreeMergeRouteDirect, "", true, WorktreeMergeRouteAuto, ""},
		{"PR deferral does not adopt CI", "", WorktreeMergeRouteAuto, WorktreeMergeRoutePullRequest, "", false, WorktreeMergeRouteAuto, ""},
		{"no deferral preserves auto", "", WorktreeMergeRouteAuto, "", "", false, WorktreeMergeRouteAuto, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			receipt := WorktreeMergeReceipt{Route: WorktreeMergeRouteDecision{Requested: row.recorded}}
			if row.deferred != "" {
				receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: row.deferred, DirectCIPullRequest: "recorded-ci"}
			}
			before := receipt
			if receipt.ValidationDeferral != nil {
				deferral := *receipt.ValidationDeferral
				before.ValidationDeferral = &deferral
			}
			options := WorktreeMergeLandOptions{Route: row.current, DirectCIPullRequest: row.link, ValidateLocally: row.local}
			applyRecordedWorktreeMergeRouteBeforeFirstResolve(&receipt, &options)
			if options.Route != row.wantRoute || options.DirectCIPullRequest != row.wantLink || options.ValidateLocally != row.local {
				t.Fatalf("recovered options = %+v", options)
			}
			if !reflect.DeepEqual(receipt, before) {
				t.Fatalf("recovery mutated recorded authority: %+v", receipt)
			}
		})
	}
}

func TestCompletedMergeReceiptRecoveryPreservesUnverifiedFailure(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name    string
		cleanup bool
		task    string
		cleaned []string
		want    string
	}{
		{"no failure remains a no-op", false, "owned-task", nil, ""},
		{"no cleanup intent", false, "owned-task", []string{"owned-task"}, "no cleanup intent"},
		{"no task evidence", true, "", nil, "incomplete"},
		{"empty cleaned identity", true, "owned-task", []string{""}, "identities are inconsistent"},
		{"wrong cleaned member", true, "owned-task", []string{"other-task"}, "did not terminalize task owned-task"},
		{"missing terminal report", true, "owned-task", []string{"owned-task"}, "cleanup reports are inconsistent"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "receipt.json")
			receipt := WorktreeMergeReceipt{ReceiptPath: path, Repository: "acme/app", Failure: "original refusal", Cleanup: row.cleanup, Candidate: WorktreeMergeCandidate{Task: row.task}, CleanedTasks: row.cleaned}
			if row.want == "" {
				receipt.Failure = ""
			}
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			original := receipt
			original.CleanedTasks = append([]string(nil), receipt.CleanedTasks...)
			err = normalizeCompletedWorktreeMergeReceipt(&receipt)
			if row.want == "" {
				if err != nil {
					t.Fatalf("no-op normalization = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("normalization error = %v", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) || !reflect.DeepEqual(receipt, original) {
				t.Fatalf("unverified failure mutated: %+v, read=%v", receipt, readErr)
			}
		})
	}
}

func TestMergeReceiptPathRecoveryIgnoresNonAuthorityEntries(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	home, err := wbhome.Root(projects)
	if err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(home, "reports", "worktree-merge")
	if err = os.MkdirAll(filepath.Join(reports, "000-directory.json"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"001-not-json.txt": "not a receipt", "003-corrupt.json": "{"} {
		if err = os.WriteFile(filepath.Join(reports, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	candidate := t.TempDir()
	sidecar := filepath.Join(reports, "002-sidecar"+worktreeMergeLandedFailureAcknowledgementSuffix)
	if err = persistWorktreeMergeReceipt(WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: sidecar, Candidate: WorktreeMergeCandidate{Worktree: candidate}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(reports, "999-owned.json")
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path, Candidate: WorktreeMergeCandidate{Worktree: candidate}}
	if err = persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveWorktreeMergeReceiptPath(projects, candidate)
	if err != nil || got != path {
		t.Fatalf("candidate resolution = %q, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("resolution changed receipt: %v", err)
	}
}

func TestMergeReceiptPathRecoveryPropagatesPhysicalStoreErrors(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"invalid root", "reports is a file"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			projects := t.TempDir()
			if kind == "invalid root" {
				projects = filepath.Join(projects, "invalid\x00root")
			} else {
				home, err := wbhome.Root(projects)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(filepath.Join(home, "reports"), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(home, "reports", "worktree-merge"), []byte("owned obstruction"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := resolveWorktreeMergeReceiptPath(projects, t.TempDir())
			if got != "" || err == nil {
				t.Fatalf("unreadable authority = %q, %v", got, err)
			}
			if _, ok := err.(*os.PathError); !ok {
				t.Fatalf("physical error lost: %T %v", err, err)
			}
		})
	}
}

func TestMergeReceiptPublicationRefusesBeforeStaging(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing path", "parent is a file", "unmarshalable time"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "receipt.json")
			before := []byte("previous receipt bytes\n")
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			priorInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			receipt := WorktreeMergeReceipt{ReceiptPath: path}
			switch kind {
			case "missing path":
				receipt.ReceiptPath = ""
			case "parent is a file":
				receipt.ReceiptPath = filepath.Join(path, "child.json")
			case "unmarshalable time":
				receipt.UpdatedAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
			}
			publicationErr := persistWorktreeMergeReceiptInjected(receipt, nil)
			if publicationErr == nil {
				t.Fatal("publication accepted invalid precondition")
			}
			switch kind {
			case "missing path":
				if publicationErr.Error() != "merge receipt path is required" {
					t.Fatalf("missing-path error = %v", publicationErr)
				}
			case "parent is a file":
				var pathErr *os.PathError
				if !errors.As(publicationErr, &pathErr) || pathErr.Path != path {
					t.Fatalf("mkdir error = %v", publicationErr)
				}
			case "unmarshalable time":
				var marshalErr *json.MarshalerError
				if !errors.As(publicationErr, &marshalErr) {
					t.Fatalf("marshal error = %v", publicationErr)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("prior receipt changed: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode() != priorInfo.Mode() {
				t.Fatalf("prior receipt mode: %v, %v", info, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "receipt.json" {
				t.Fatalf("staging debris = %v, %v", entries, err)
			}
		})
	}
}

func TestMergeReceiptPathRecoveryPreservesAbsFailurePrecedence(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"  relative-candidate  ", " \t "} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			projects := filepath.Join(t.TempDir(), "invalid\x00root")
			sentinel := errors.New("owned Abs failure")
			calls := 0
			got, err := resolveWorktreeMergeReceiptPathWithAbs(projects, input, func(value string) (string, error) {
				calls++
				if value != "relative-candidate" {
					t.Fatalf("Abs input = %q", value)
				}
				return "", sentinel
			})
			if strings.TrimSpace(input) == "" {
				if got != "" || err == nil || err.Error() != "candidate worktree or receipt is required" || calls != 0 {
					t.Fatalf("empty input = %q, %v, calls=%d", got, err, calls)
				}
			} else if got != "" || err != sentinel || calls != 1 {
				t.Fatalf("Abs failure = %q, %v, calls=%d", got, err, calls)
			}
			entries, readErr := os.ReadDir(filepath.Dir(projects))
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("failed resolution touched authority: %v, %v", entries, readErr)
			}
		})
	}
}
