package cmdfleet

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/defaultbranch"
)

func TestDefaultBranchCLIReportsRemoteFailureAndWriterFailure(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{}
	deps := fakeDependencies()
	reads := 0
	deps.DefaultBranch = func(_ context.Context, request defaultbranch.Request, _ io.Writer) (defaultbranch.Report, error) {
		reads++
		if len(request.Repositories) != 1 || request.Repositories[0] != "acme/app" || request.Branch != "main" || request.Parallel != 1 {
			t.Fatal(request)
		}
		return defaultbranch.Report{Desired: "main", Repositories: []defaultbranch.Repository{{Repository: "acme/app", Disposition: "error", Error: "injected metadata failure"}}, Summary: defaultbranch.Summary{Errors: 1}}, nil
	}
	for _, test := range []struct {
		name string
		args []string
		out  *bytes.Buffer
		want string
	}{
		{name: "text", out: new(bytes.Buffer), want: "default-branch findings remain"},
		{name: "json", args: []string{"--json"}, out: new(bytes.Buffer), want: "default-branch findings remain"},
	} {
		//nolint:paralleltest // Rows reuse the parent shared.Flags pointer and dependency callbacks/counters; preserve sequential parse/execute/assert order.
		t.Run(test.name, func(t *testing.T) {
			command := fakeRoot(&flags, deps)
			command.SetOut(test.out)
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(append([]string{"--projects-root", "/fixture/projects", "fleet", "default-branch", "--repo", "acme/app", "--branch", "main", "--parallel", "1"}, test.args...))
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(test.out.String(), "acme/app") || !strings.Contains(test.out.String(), "injected metadata failure") {
				t.Fatalf("command output=%q err=%v, want report and %q", test.out.String(), err, test.want)
			}
		})
	}
	if reads != 2 {
		t.Fatalf("remote metadata reads=%d, want one per command", reads)
	}
	for _, format := range []string{"text", "json"} {
		//nolint:paralleltest // Rows reuse the parent shared.Flags pointer and dependency callbacks/counters; preserve sequential parse/execute/assert order.
		t.Run("writer failure "+format, func(t *testing.T) {
			command := fakeRoot(&flags, deps)
			writer := &defaultBranchFailWriter{failAt: 1}
			command.SetOut(writer)
			command.SetErr(&bytes.Buffer{})
			args := []string{"--projects-root", "/fixture/projects", "fleet", "default-branch", "--repo", "acme/app", "--branch", "main", "--parallel", "1"}
			if format == "json" {
				args = append(args, "--json")
			}
			command.SetArgs(args)
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "output unavailable") || writer.writes != 1 {
				t.Fatalf("writer failure: writes=%d err=%v", writer.writes, err)
			}
		})
	}
	if reads != 4 {
		t.Fatalf("remote metadata reads=%d, want one per command", reads)
	}
}
