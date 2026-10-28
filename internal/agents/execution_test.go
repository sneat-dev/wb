package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

var executionTestImage = "ghcr.io/acme/agent@sha256:" + strings.Repeat("a", 64)

func executionTestPolicy() ExecutionPolicy {
	return ExecutionPolicy{
		Mode:            ExecutionModeLinuxContainer,
		Image:           executionTestImage,
		MemoryBytes:     8589934592,
		MemorySwapBytes: 8589934592,
		CPUCount:        2,
		PIDsLimit:       128,
		MaxParallel:     1,
	}
}

// executionYAML writes a complete agents.execution block whose fields a test
// then changes one at a time.
func executionYAML(mode, image string, memory, swap int64, cpus, pids, parallel int) string {
	return fmt.Sprintf(`agents:
  execution:
    mode: %q
    image: %q
    memory_bytes: %d
    memory_swap_bytes: %d
    cpu_count: %d
    pids_limit: %d
    max_parallel: %d
`, mode, image, memory, swap, cpus, pids, parallel)
}

func TestLoadConfigFileAcceptsAMandatoryLinuxContainerPolicy(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1)))
	if err != nil {
		t.Fatalf("a complete mandatory policy must load: %v", err)
	}
	if config.Execution == nil {
		t.Fatal("the configured execution policy was dropped")
	}
	if *config.Execution != executionTestPolicy() {
		t.Fatalf("policy = %#v", *config.Execution)
	}
}

func TestExecutionPolicyAcceptsOnlyDigestPinnedImages(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("b", 64)
	cases := map[string]string{
		"registry reference":        "ghcr.io/acme/agent@sha256:" + hash,
		"tag plus digest is pinned": "ghcr.io/acme/agent:2026.10.31@sha256:" + hash,
		"bare image ID":             "sha256:" + hash,
		"docker hub library":        "alpine@sha256:" + hash,
	}
	for name, image := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config, err := LoadConfigFile(writeConfig(t, executionYAML(ExecutionModeLinuxContainer, image, 8589934592, 8589934592, 2, 128, 1)))
			if err != nil {
				t.Fatalf("image %q was refused: %v", image, err)
			}
			if config.Execution == nil || config.Execution.Image != image {
				t.Fatalf("policy = %#v", config.Execution)
			}
		})
	}
}

func TestLoadConfigFileRefusesMalformedExecutionPolicies(t *testing.T) {
	t.Parallel()
	mode := ExecutionModeLinuxContainer
	image := executionTestImage
	valid := executionYAML(mode, image, 8589934592, 8589934592, 2, 128, 1)
	cases := map[string]struct {
		body string
		want string
	}{
		"unknown mode": {
			body: executionYAML("windows-container", image, 8589934592, 8589934592, 2, 128, 1),
			want: `mode "windows-container" is not supported`,
		},
		"missing mode": {
			body: executionYAML("", image, 8589934592, 8589934592, 2, 128, 1),
			want: "mode is required",
		},
		"missing image": {
			body: executionYAML(mode, "", 8589934592, 8589934592, 2, 128, 1),
			want: "image is required",
		},
		"mutable tag": {
			body: executionYAML(mode, "ghcr.io/acme/agent:latest", 8589934592, 8589934592, 2, 128, 1),
			want: "is a mutable tag",
		},
		"untagged reference": {
			body: executionYAML(mode, "ghcr.io/acme/agent", 8589934592, 8589934592, 2, 128, 1),
			want: "is a mutable tag",
		},
		"short digest": {
			body: executionYAML(mode, "ghcr.io/acme/agent@sha256:abcdef", 8589934592, 8589934592, 2, 128, 1),
			want: "invalid sha256 digest",
		},
		"uppercase digest": {
			body: executionYAML(mode, "ghcr.io/acme/agent@sha256:"+strings.Repeat("A", 64), 8589934592, 8589934592, 2, 128, 1),
			want: "invalid sha256 digest",
		},
		"non-sha256 digest": {
			body: executionYAML(mode, "ghcr.io/acme/agent@sha512:"+strings.Repeat("c", 64), 8589934592, 8589934592, 2, 128, 1),
			want: "pinned by a sha256 digest",
		},
		"empty repository before digest": {
			body: executionYAML(mode, "@sha256:"+strings.Repeat("d", 64), 8589934592, 8589934592, 2, 128, 1),
			want: "no repository before its digest",
		},
		"zero memory": {
			body: executionYAML(mode, image, 0, 8589934592, 2, 128, 1),
			want: "memory_bytes must be a positive",
		},
		"negative memory": {
			body: executionYAML(mode, image, -1, 8589934592, 2, 128, 1),
			want: "memory_bytes must be a positive",
		},
		"zero swap": {
			body: executionYAML(mode, image, 8589934592, 0, 2, 128, 1),
			want: "memory_swap_bytes must be a positive",
		},
		"swap above memory": {
			body: executionYAML(mode, image, 8589934592, 8589934593, 2, 128, 1),
			want: "must equal memory_bytes",
		},
		"swap below memory": {
			body: executionYAML(mode, image, 8589934592, 4294967296, 2, 128, 1),
			want: "must equal memory_bytes",
		},
		"zero cpus": {
			body: executionYAML(mode, image, 8589934592, 8589934592, 0, 128, 1),
			want: "cpu_count must be a positive",
		},
		"negative cpus": {
			body: executionYAML(mode, image, 8589934592, 8589934592, -2, 128, 1),
			want: "cpu_count must be a positive",
		},
		"zero pids": {
			body: executionYAML(mode, image, 8589934592, 8589934592, 2, 0, 1),
			want: "pids_limit must be a positive",
		},
		"zero parallel": {
			body: executionYAML(mode, image, 8589934592, 8589934592, 2, 128, 0),
			want: "max_parallel must be a positive",
		},
		"unsupported parallelism": {
			body: executionYAML(mode, image, 8589934592, 8589934592, 2, 128, 2),
			want: "max_parallel 2 is unsupported",
		},
		"arbitrary runtime arguments": {
			body: valid + "    docker_args: [--privileged]\n",
			want: `field "docker_args" is not supported`,
		},
		"mount escape": {
			body: valid + "    mounts: [/:/host]\n",
			want: `field "mounts" is not supported`,
		},
		"security escape": {
			body: valid + "    security_opt: [seccomp=unconfined]\n",
			want: `field "security_opt" is not supported`,
		},
		"unknown casing": {
			body: valid + "    memoryBytes: 8589934592\n",
			want: `field "memoryBytes" is not supported`,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config, err := LoadConfigFile(writeConfig(t, testCase.body))
			if err == nil {
				t.Fatalf("%s was accepted as %#v", name, config.Execution)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestSupportedExecutionFieldsMatchThePolicyType(t *testing.T) {
	t.Parallel()
	raw, err := yaml.Marshal(executionTestPolicy())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 7 {
		t.Fatalf("marshalled fields = %#v", fields)
	}
	for field := range fields {
		if !supportedExecutionField(field) {
			t.Fatalf("field %q is marshalled but not listed as supported", field)
		}
		if !strings.Contains(supportedExecutionFields, field) {
			t.Fatalf("field %q is not named in %q", field, supportedExecutionFields)
		}
	}
}

func TestConfigWithoutExecutionPolicyKeepsHostExecution(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    p: { harness: codex, provider: deepseek, model: m }
  task_types:
    coding: { profile: p }
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Execution != nil {
		t.Fatalf("an absent policy must stay absent, got %#v", config.Execution)
	}
	plan, err := (Planner{Config: config}).Plan(RunRequest{Task: "x", TaskType: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Execution != nil {
		t.Fatalf("plan invented a policy: %#v", plan.Execution)
	}
	missing, err := LoadConfigFile(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil || missing.Execution != nil {
		t.Fatalf("a missing configuration must stay host-executable: %#v %v", missing.Execution, err)
	}
	// A record written without a policy must not grow one, and its JSON must
	// not claim a sandbox that never applied.
	store := NewStore(t.TempDir())
	agentID, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{AgentID: agentID, State: StateCompleted, ExecutionStatus: StatusCompleted, StartedAt: time.Unix(1700000000, 0).UTC()}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.RecordPath(agentID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"execution"`) {
		t.Fatalf("an absent policy must be omitted from the record: %s", raw)
	}
}

func TestPlanSnapshotsTheMandatoryPolicyForEveryRoute(t *testing.T) {
	t.Parallel()
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1) + `  profiles:
    coder: { harness: codex, provider: deepseek, model: m }
    alt: { harness: dsh, model: deepseek-v4-flash }
  task_types:
    coding: { profile: coder, fallback: [alt] }
  fallback: [alt]
`
	config, err := LoadConfigFile(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	want := executionTestPolicy()
	cases := map[string]RunRequest{
		"explicit profile":       {Task: "x", Profile: "coder"},
		"explicit harness/model": {Task: "x", Harness: HarnessOpenCode, Model: "opencode-go/m"},
		"task type":              {Task: "x", TaskType: "coding"},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plan, err := (Planner{Config: config}).Plan(request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Execution == nil || *plan.Execution != want {
				t.Fatalf("policy = %#v, want %#v", plan.Execution, want)
			}
			if plan.Execution == config.Execution {
				t.Fatal("the plan must snapshot the policy, not share the configuration's pointer")
			}
		})
	}
	// A preflight fallback swaps the profile. The mandatory policy is not a
	// profile property, so it must survive unchanged.
	fallbackPlan, err := (Planner{Config: config, Probe: func(resolved Resolved) error {
		if resolved.Profile == "coder" {
			return errors.New("unavailable")
		}
		return nil
	}}).Plan(RunRequest{Task: "x", TaskType: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if fallbackPlan.FallbackFrom != "coder" || fallbackPlan.Profile.Profile != "alt" {
		t.Fatalf("fallback = %#v", fallbackPlan)
	}
	if fallbackPlan.Execution == nil || *fallbackPlan.Execution != want {
		t.Fatalf("fallback policy = %#v, want %#v", fallbackPlan.Execution, want)
	}
}

func TestPlanPolicySnapshotSurvivesALaterConfigurationEdit(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1)+`  profiles:
    coder: { harness: codex, provider: deepseek, model: m }
  task_types:
    coding: { profile: coder }
`))
	if err != nil {
		t.Fatal(err)
	}
	want := executionTestPolicy()
	plan, err := (Planner{Config: config}).Plan(RunRequest{Task: "x", TaskType: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	config.Execution.MemoryBytes = 1
	config.Execution.Image = "ghcr.io/acme/other@sha256:" + strings.Repeat("e", 64)
	if plan.Execution == nil || *plan.Execution != want {
		t.Fatalf("a configuration edit rewrote the resolved plan: %#v", plan.Execution)
	}
}

func TestRecordAndResultRoundTripTheMandatoryPolicy(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	store := NewStore(t.TempDir())
	agentID, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		AgentID: agentID, State: StateCompleted, ExecutionStatus: StatusCompleted,
		Execution: &policy, StartedAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(agentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Execution == nil || *loaded.Execution != policy {
		t.Fatalf("loaded policy = %#v", loaded.Execution)
	}
	raw, err := os.ReadFile(store.RecordPath(agentID))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"execution"`, `"mode": "linux-container"`, `"memory_swap_bytes": 8589934592`, `"max_parallel": 1`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("record %s is missing %q", raw, want)
		}
	}
	rendered := store.Render(loaded)
	if rendered.Execution == nil || *rendered.Execution != policy {
		t.Fatalf("rendered policy = %#v", rendered.Execution)
	}
}

func TestDryRunPlanDocumentCarriesTheMandatoryPolicy(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	raw, err := json.Marshal(struct {
		Plan Plan `json:"plan"`
	}{Plan: Plan{Execution: &policy}})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Plan struct {
			Execution *ExecutionPolicy `json:"execution"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.Plan.Execution == nil || *document.Plan.Execution != policy {
		t.Fatalf("dry-run plan policy = %#v", document.Plan.Execution)
	}
	// A host plan must not serialize an execution claim at all.
	hostRaw, err := json.Marshal(struct {
		Plan Plan `json:"plan"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hostRaw), `"execution"`) {
		t.Fatalf("a host dry run must not claim a sandbox: %s", hostRaw)
	}
}

func TestDispatchRefusesToRunOutsideAConfiguredMandatorySandbox(t *testing.T) {
	// Not parallel: the dispatch fixture pins environment variables.
	fixture := newDispatchFixture(t)
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1) + `  profiles:
    cheap: { harness: codex, provider: deepseek, model: deepseek-flash }
`
	fixture.deps.LoadConfig = func() (Config, error) {
		return LoadConfigFile(writeConfig(t, body))
	}
	_, err := fixture.dispatch(t, newRequest())
	if err == nil {
		t.Fatal("a mandatory sandbox this build cannot enforce must refuse the dispatch")
	}
	if !strings.Contains(err.Error(), "refusing to run it outside the mandatory sandbox") {
		t.Fatalf("error = %v", err)
	}
	if fixture.createCalls != 0 || fixture.beforeCalls != 0 || fixture.spawnedAgentID != "" {
		t.Fatalf("a refused dispatch started side effects: create=%d before=%d spawn=%q", fixture.createCalls, fixture.beforeCalls, fixture.spawnedAgentID)
	}
	records, err := NewStore(fixture.home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("a refused dispatch persisted a run: %#v", records)
	}
}

func TestDispatchRefusesAPlanWhosePolicyDoesNotMatchConfiguration(t *testing.T) {
	// Not parallel: the dispatch fixture pins environment variables, and the
	// subtests replace that fixture's configuration per case.
	configured := executionTestPolicy()
	other := configured
	other.MemoryBytes, other.MemorySwapBytes = 4294967296, 4294967296
	cases := map[string]struct {
		configured *ExecutionPolicy
		planned    *ExecutionPolicy
		want       string
	}{
		"configured but the plan has no snapshot": {
			configured: &configured, planned: nil, want: "carries no mandatory policy snapshot",
		},
		"plan still carries a removed policy": {
			configured: nil, planned: &configured, want: "no longer declares",
		},
		"plan policy differs from configuration": {
			configured: &configured, planned: &other, want: "changed after this plan was resolved",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newDispatchFixture(t)
			fixture.deps.LoadConfig = func() (Config, error) {
				return Config{
					Profiles:  map[string]Profile{"cheap": {Harness: HarnessCodex, Provider: "deepseek", Model: "deepseek-flash"}},
					TaskTypes: builtinTaskTypes(),
					Execution: testCase.configured,
				}, nil
			}
			plan := Plan{Profile: Resolved{Profile: "cheap", Harness: HarnessCodex, Provider: "deepseek", Model: "deepseek-flash"}, Execution: testCase.planned}
			_, err := fixture.dispatch(t, DispatchRequest{
				Mode: ModeNew, Worktree: "task-one", Task: "x", Timeout: time.Minute, Plan: &plan,
			})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
			if fixture.createCalls != 0 || fixture.spawnedAgentID != "" {
				t.Fatalf("a refused dispatch started side effects: create=%d spawn=%q", fixture.createCalls, fixture.spawnedAgentID)
			}
		})
	}
}
