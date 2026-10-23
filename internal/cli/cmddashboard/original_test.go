package cmddashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
)

func dashboardRuntime(flags *shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return *flags }, ExitError: func(code int, message string) error { return fmt.Errorf("exit %d: %s", code, message) }}
}
func TestDashboardOpensHostedURL(t *testing.T) {
	t.Parallel()
	var opened string
	command := New(dashboardRuntime(&shared.Flags{}), Operations{
		Open: func(target string) error { opened = target; return nil },
		LocalURL: func(context.Context, string) (string, string, error) {
			return "", "", errors.New("unexpected local lookup")
		},
	})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if opened != hostedDashboardURL || !strings.Contains(output.String(), hostedDashboardURL) {
		t.Fatalf("opened = %q, output = %q", opened, output.String())
	}
}

// TestDashboardNonInteractiveDoesNotOpenBrowser proves that an invocation
// carrying nonInteractive: true (as --non-interactive sets it) stops "wb
// dashboard" from opening a browser even in text format, where an
// interactive invocation would: a mutation that swapped inv for a fresh
// &invocation{} (sneat-dev/wb#733 PR-3 review, finding N1) would make this
// test's open dependency fire, which it must not.
func TestDashboardNonInteractiveDoesNotOpenBrowser(t *testing.T) {
	t.Parallel()
	opened := false
	command := New(dashboardRuntime(&shared.Flags{NonInteractive: true}), Operations{
		Open: func(string) error { opened = true; return nil },
		LocalURL: func(context.Context, string) (string, string, error) {
			return "", "", errors.New("unexpected local lookup")
		},
	})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if opened || !strings.Contains(output.String(), hostedDashboardURL) {
		t.Fatalf("opened = %t, output = %q, want the URL printed but not opened", opened, output.String())
	}
}
func TestDashboardJSONDoesNotOpenBrowser(t *testing.T) {
	t.Parallel()
	opened := false
	command := New(dashboardRuntime(&shared.Flags{}), Operations{
		Open: func(string) error { opened = true; return nil },
		LocalURL: func(context.Context, string) (string, string, error) {
			return "", "", errors.New("unexpected local lookup")
		},
	})
	command.SetArgs([]string{"--format=json"})
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result dashboardOpenResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if opened || result.Opened || result.Scope != "hosted" || result.URL != hostedDashboardURL {
		t.Fatalf("opened = %t, result = %+v", opened, result)
	}
}
func TestDashboardLocalStartsDaemonAndOpensItsURL(t *testing.T) {
	t.Parallel()
	wantRoot := t.TempDir()
	var opened, root string
	command := New(dashboardRuntime(&shared.Flags{ProjectsRoot: wantRoot}), Operations{
		Open: func(target string) error { opened = target; return nil },
		LocalURL: func(_ context.Context, projectsRoot string) (string, string, error) {
			root = projectsRoot
			return "http://127.0.0.1:9000/", "", nil
		},
	})
	command.SetArgs([]string{"--local"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if root != wantRoot || opened != "http://127.0.0.1:9000/cockpit/" {
		t.Fatalf("root = %q, opened = %q, want root = %q", root, opened, wantRoot)
	}
}

// wb dashboard --local must not silently drop the item-9 warning an implicit
// Start returns when it found a live, healthy, supervised daemon under a
// different binary than this invocation's own (sneat-dev/wb#622 review
// round 3, item M1).
func TestDashboardLocalPrintsAProvenanceWarningToStderr(t *testing.T) {
	t.Parallel()
	command := New(dashboardRuntime(&shared.Flags{}), Operations{
		Open: func(string) error { return nil },
		LocalURL: func(context.Context, string) (string, string, error) {
			return "http://127.0.0.1:9000/", "the running supervised daemon's executable does not match this invocation's own binary", nil
		},
	})
	command.SetArgs([]string{"--local"})
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "does not match this invocation's own binary") {
		t.Fatalf("stderr = %q, want the warning printed", stderr.String())
	}
}
func TestDashboardLocalSaysItIsDeprecatedInFavourOfCockpit(t *testing.T) {
	t.Parallel()
	command := New(dashboardRuntime(&shared.Flags{NonInteractive: true}), Operations{
		Open: func(string) error { return nil },
		LocalURL: func(context.Context, string) (string, string, error) {
			return "http://127.0.0.1:9000/cockpit/", "", nil
		},
	})
	command.SetArgs([]string{"--local"})
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "deprecated") || !strings.Contains(stderr.String(), "wb cockpit") {
		t.Fatalf("stderr = %q, want the deprecation naming wb cockpit", stderr.String())
	}
}
func TestDashboardHostedSaysNothingOnStderr(t *testing.T) {
	t.Parallel()
	command := New(dashboardRuntime(&shared.Flags{NonInteractive: true}), Operations{
		Open: func(string) error { return nil },
		LocalURL: func(context.Context, string) (string, string, error) {
			return "", "", errors.New("unexpected local lookup")
		},
	})
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want none", stderr.String())
	}
}
func TestLocalCockpitURLIsTheCockpitMountOfTheDaemonAddress(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"http://127.0.0.1:9000/", "http://127.0.0.1:9000/cockpit/"} {
		got, err := localCockpitURL(base)
		if want := "http://127.0.0.1:9000/cockpit/"; err != nil || got != want {
			t.Errorf("localCockpitURL(%q) = %q, %v, want %q", base, got, err, want)
		}
	}
	if got, err := localCockpitURL("http://%zz/"); err == nil || got != "" {
		t.Errorf("an address that does not parse = %q, %v, want an error and no address", got, err)
	}
}
func TestDashboardLocalRefusesADaemonAddressThatDoesNotParse(t *testing.T) {
	t.Parallel()
	command := New(dashboardRuntime(&shared.Flags{NonInteractive: true}), Operations{
		Open: func(string) error { return nil },
		LocalURL: func(context.Context, string) (string, string, error) {
			return "http://%zz/", "", nil
		},
	})
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--local"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "daemon address") {
		t.Fatalf("Execute() error = %v, want a daemon address error", err)
	}
}
