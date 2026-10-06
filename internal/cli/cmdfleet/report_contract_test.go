package cmdfleet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/sneat-dev/wb/internal/fleetinspect"
	"github.com/sneat-dev/wb/internal/mergepolicy"
	"github.com/sneat-dev/wb/internal/prinventory"
)

type operationContextKey struct{}

func TestTypedInventoryDatesFailBeforeReportOrOutputEffects(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"snapshot", "created", "updated"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			report := prinventory.Report{Complete: true}
			invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			switch field {
			case "snapshot":
				report.SnapshotAt = invalid
			case "created":
				report.PullRequests = []prinventory.PullRequest{{CreatedAt: invalid}}
			case "updated":
				report.PullRequests = []prinventory.PullRequest{{UpdatedAt: invalid}}
			}
			deps := fakeDependencies()
			deps.Inventory = func(context.Context, prinventory.Options) prinventory.Report { return report }
			deps.MkdirAll = func(string, os.FileMode) error {
				t.Fatal("directory effect reached after typed marshal error")
				return nil
			}
			deps.WriteFile = func(string, []byte, os.FileMode) error {
				t.Fatal("file effect reached after typed marshal error")
				return nil
			}
			flags := shared.Flags{}
			stdout, _, err := execute(t, NewPRs(testRuntime(&flags), deps), "--report-dir", "/report")
			if err == nil || !strings.Contains(err.Error(), "year outside of range") || stdout != "" {
				t.Fatal(stdout, err)
			}
		})
	}
}
func TestFleetReportFilesPrecedeLateFormatAndWriterErrors(t *testing.T) {
	t.Parallel()
	for _, overview := range []bool{false, true} {
		for _, format := range []string{"markdown", "yaml", "json", "toml"} {
			t.Run(format+map[bool]string{true: "overview", false: "stats"}[overview], func(t *testing.T) {
				t.Parallel()
				deps := fakeDependencies()
				files := map[string][]byte{}
				deps.MkdirAll = func(_ string, mode os.FileMode) error {
					if mode != 0o755 {
						t.Fatal(mode)
					}
					return nil
				}
				deps.WriteFile = func(path string, data []byte, mode os.FileMode) error {
					if mode != 0o644 {
						t.Fatal(mode)
					}
					files[path] = append([]byte(nil), data...)
					return nil
				}
				var out bytes.Buffer
				var err error
				if overview {
					err = writeFleetOverviewOutput(&out, deps, fleetinspect.OverviewReport{SchemaVersion: 1}, format, "/report", false)
				} else {
					err = writeFleetStatsOutput(&out, deps, fleetinspect.StatsReport{SchemaVersion: 1}, format, "/report")
				}
				if len(files) != 2 {
					t.Fatal(files)
				}
				if format == "toml" {
					if err == nil || !strings.Contains(err.Error(), "unknown --format") || out.Len() != 0 {
						t.Fatal(out.String(), err)
					}
				} else if err != nil || out.Len() == 0 {
					t.Fatal(out.String(), err)
				}
			})
		}
	}
}
func TestFleetReportFailuresPreserveEffectOrder(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("storage failed")
	for _, kind := range []string{"stats", "overview", "prs"} {
		for failAt := 1; failAt <= 3; failAt++ {
			t.Run(kind+string(rune('0'+failAt)), func(t *testing.T) {
				t.Parallel()
				calls := 0
				deps := fakeDependencies()
				deps.MkdirAll = func(string, os.FileMode) error {
					calls++
					if calls == failAt {
						return sentinel
					}
					return nil
				}
				deps.WriteFile = func(string, []byte, os.FileMode) error {
					calls++
					if calls == failAt {
						return sentinel
					}
					return nil
				}
				var out bytes.Buffer
				var err error
				switch kind {
				case "stats":
					err = writeFleetStatsOutput(&out, deps, fleetinspect.StatsReport{}, "markdown", "/report")
				case "overview":
					err = writeFleetOverviewOutput(&out, deps, fleetinspect.OverviewReport{}, "markdown", "/report", true)
				case "prs":
					err = writePRInventoryOutput(&out, deps, prinventory.Report{}, "markdown", "/report")
				}
				if !errors.Is(err, sentinel) || calls != failAt || out.Len() != 0 {
					t.Fatal(calls, err, out.String())
				}
			})
		}
	}
}
func TestCommandOperationsReadParsedFlagsAndKeepInstanceDefaults(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"stats", "overview", "merge-policy", "default-branch", "prs"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{}
			deps := fakeDependencies()
			called := 0
			ctx := context.WithValue(t.Context(), operationContextKey{}, kind)
			var expectedErr = errors.New("delegated failure")
			deps.Stats = func(got context.Context, r fleetinspect.Request) (fleetinspect.StatsReport, error) {
				called++
				if got != ctx || r.ProjectsRoot != "parsed" || r.Filter != "selected" || r.Parallel != 4 || r.Match != "" || r.Regex != "" || r.Remote || r.Hooks || r.All {
					t.Fatal(r)
				}
				return fleetinspect.StatsReport{}, expectedErr
			}
			deps.Overview = func(got context.Context, r fleetinspect.Request) (fleetinspect.OverviewReport, error) {
				called++
				if got != ctx || r.ProjectsRoot != "parsed" || r.Filter != "selected" || r.Parallel != 4 || r.Remote || r.Hooks {
					t.Fatal(r)
				}
				return fleetinspect.OverviewReport{}, expectedErr
			}
			deps.DefaultBranch = func(got context.Context, r defaultbranch.Request, w io.Writer) (defaultbranch.Report, error) {
				called++
				if got != ctx || r.Scope != (defaultbranch.Scope{ProjectsRoot: "parsed", Filter: "selected"}) || !reflect.DeepEqual(r.Options, defaultbranch.Options{Parallel: 4}) {
					t.Fatal(r)
				}
				return defaultbranch.Report{}, expectedErr
			}
			deps.MergePolicy = func(got context.Context, r mergepolicy.Request, w io.Writer) (mergepolicy.Report, error) {
				called++
				if got != ctx || r.Scope != (mergepolicy.Scope{ProjectsRoot: "parsed", Filter: "selected"}) || !reflect.DeepEqual(r.Options, mergepolicy.Options{Parallel: 8}) {
					t.Fatal(r)
				}
				return mergepolicy.Report{}, expectedErr
			}
			deps.InventoryOwners = func(extra []string) ([]prinventory.Owner, []prinventory.Diagnostic) {
				if !reflect.DeepEqual(extra, []string{"late"}) {
					t.Fatal(extra)
				}
				return []prinventory.Owner{{Login: "owner"}}, nil
			}
			deps.Inventory = func(got context.Context, r prinventory.Options) prinventory.Report {
				called++
				if got != ctx || r.Parallel != 8 || r.ExcludeArchived || r.CreatedBefore != "" || len(r.Owners) != 1 {
					t.Fatal(r)
				}
				return prinventory.Report{Complete: true}
			}
			command := New(testRuntime(&flags), deps)
			flags = shared.Flags{ProjectsRoot: "parsed", Filter: "selected", ExtraOrgs: []string{"late"}}
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs([]string{kind})
			err := command.ExecuteContext(ctx)
			if called != 1 || (kind != "prs" && !errors.Is(err, expectedErr)) || (kind == "prs" && err != nil) {
				t.Fatal(called, err)
			}
		})
	}
}
