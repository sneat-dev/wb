package cmdagent

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
)

// runFixture fills the run and discovery operations the new commands need.

func TestCompareParsesProfilesAndRepeat(t *testing.T) {
	t.Parallel()
	deps, _, compareRequests := runFixture(t)
	code, out, errout := runFake(t, deps, "agent", "compare", "--profiles", "cheap, op", "--repeat", "2", "--concurrency", "1", "--task", "compare it")
	if code != exitOK {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
	request := (*compareRequests)[0]
	if len(request.Profiles) != 2 || request.Profiles[0] != "cheap" || request.Profiles[1] != "op" {
		t.Fatalf("profiles = %#v", request.Profiles)
	}
	if request.Repeat != 2 || request.Concurrency != 1 {
		t.Fatalf("repeat/concurrency = %d/%d", request.Repeat, request.Concurrency)
	}
	if !strings.Contains(out, "experiment exp-") || !strings.Contains(out, "STATUS") || !strings.Contains(out, "summary") {
		t.Fatalf("output = %q", out)
	}
}

func TestCompareRequiresDistinctProfiles(t *testing.T) {
	t.Parallel()
	deps, _, _ := runFixture(t)
	for _, profiles := range []string{"", "cheap,cheap", " , "} {
		code, _, errout := runFake(t, deps, "agent", "compare", "--profiles", profiles, "--task", "x")
		if code != exitUsage {
			t.Fatalf("profiles %q: code = %d stderr=%s", profiles, code, errout)
		}
	}
}

func TestCompareReportsARequestRefusalAsUsage(t *testing.T) {
	t.Parallel()
	deps, _, _ := runFixture(t)
	deps.Compare = func(context.Context, agentrun.CompareRequest) (agents.ExperimentReport, error) {
		return agents.ExperimentReport{}, agents.NewRequestError("unknown agent profile")
	}
	code, _, errout := runFake(t, deps, "agent", "compare", "--profiles", "cheap", "--task", "x")
	if code != exitUsage || !strings.Contains(errout, "unknown agent profile") {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
}

func TestExperimentReadsBackAndRefusesAnUnknownID(t *testing.T) {
	t.Parallel()
	deps, _, _ := runFixture(t)
	deps.Experiment = func(context.Context, agentrun.ExperimentRequest) (agents.ExperimentReport, error) {
		return agents.ExperimentReport{Experiment: agents.Experiment{ExperimentID: "exp-00000000000000000000000000000001", BaseSHA: "base"}}, nil
	}
	code, out, errout := runFake(t, deps, "agent", "experiment", "exp-00000000000000000000000000000001")
	if code != exitOK || !strings.Contains(out, "experiment exp-") {
		t.Fatalf("code = %d out=%q stderr=%s", code, out, errout)
	}
	deps.Experiment = func(context.Context, agentrun.ExperimentRequest) (agents.ExperimentReport, error) {
		return agents.ExperimentReport{}, &agents.UnknownExperimentError{ExperimentID: "exp-1", Root: "/root"}
	}
	code, _, errout = runFake(t, deps, "agent", "experiment", "exp-00000000000000000000000000000001")
	if code != exitFindings || !strings.Contains(errout, "no experiment") {
		t.Fatalf("code = %d stderr=%s", code, errout)
	}
}

func TestExperimentJSONRendersTheDocument(t *testing.T) {
	t.Parallel()
	deps, _, _ := runFixture(t)
	deps.Experiment = func(context.Context, agentrun.ExperimentRequest) (agents.ExperimentReport, error) {
		return agents.ExperimentReport{Experiment: agents.Experiment{ExperimentID: "exp-00000000000000000000000000000001"}}, nil
	}
	code, out, _ := runFake(t, deps, "agent", "experiment", "exp-00000000000000000000000000000001", "--format", "json")
	if code != exitOK || !strings.Contains(out, `"experiment_id"`) {
		t.Fatalf("code = %d out=%q", code, out)
	}
}
