package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
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
		"registry with port":        "localhost:5000/acme/agent@sha256:" + hash,
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

// TestExecutionImageRejectsMalformedRepositories pins the grammar beside the
// digest check: a valid sha256 is not enough when the repository prefix could be
// reinterpreted as a flag, a second reference or a path escape.
func TestExecutionImageRejectsMalformedRepositories(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 64)
	cases := map[string]struct {
		image string
		want  string
	}{
		"empty image":                 {image: "", want: "image is required"},
		"leading dash repository":     {image: "-ghcr.io/acme/agent@sha256:" + hash, want: "invalid registry"},
		"leading dash component":      {image: "ghcr.io/-acme/agent@sha256:" + hash, want: "invalid repository component"},
		"uppercase repository":        {image: "ghcr.io/acme/Agent@sha256:" + hash, want: "invalid repository component"},
		"empty path component":        {image: "ghcr.io//agent@sha256:" + hash, want: "empty repository component"},
		"trailing slash":              {image: "ghcr.io/acme/agent/@sha256:" + hash, want: "empty repository component"},
		"parent directory component":  {image: "ghcr.io/acme/../agent@sha256:" + hash, want: "invalid repository component"},
		"multiple digest separators":  {image: "ghcr.io/acme/agent@sha256:" + hash + "@sha256:" + hash, want: "at most one @ digest separator"},
		"control character":           {image: "ghcr.io/acme/agent\x01@sha256:" + hash, want: "printable ASCII"},
		"non-ASCII repository":        {image: "ghcr.io/acme/agënt@sha256:" + hash, want: "printable ASCII"},
		"whitespace after repository": {image: "ghcr.io/acme/agent @sha256:" + hash, want: "must not contain whitespace"},
		"scheme is not a repository":  {image: "https://ghcr.io/acme/agent@sha256:" + hash, want: "empty repository component"},
		"invalid registry port":       {image: "ghcr.io:abc/acme/agent@sha256:" + hash, want: "invalid registry port"},
		"empty registry host":         {image: ":5000/acme/agent@sha256:" + hash, want: "empty registry host"},
		"invalid registry label":      {image: "ghcr..io/acme/agent@sha256:" + hash, want: "invalid registry"},
		"empty tag":                   {image: "ghcr.io/acme/agent:@sha256:" + hash, want: "invalid tag"},
		"overlong tag":                {image: "ghcr.io/acme/agent:" + strings.Repeat("x", 129) + "@sha256:" + hash, want: "invalid tag"},
		"non-sha256 algorithm":        {image: "ghcr.io/acme/agent@sha512:" + hash, want: "pinned by a sha256 digest"},
		"short digest":                {image: "ghcr.io/acme/agent@sha256:" + hash[:63], want: "invalid sha256 digest"},
		"uppercase digest":            {image: "ghcr.io/acme/agent@sha256:" + strings.Repeat("A", 64), want: "invalid sha256 digest"},
		"non-hex digest":              {image: "ghcr.io/acme/agent@sha256:" + strings.Repeat("z", 64), want: "invalid sha256 digest"},
		"bare digest is too short":    {image: "sha256:" + hash[:63], want: "invalid sha256 digest"},
		"mutable tag":                 {image: "ghcr.io/acme/agent:latest", want: "is a mutable tag"},
		"untagged repository":         {image: "ghcr.io/acme/agent", want: "is a mutable tag"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validateExecutionImage(testCase.image)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("validateExecutionImage(%q) = %v, want %q", testCase.image, err, testCase.want)
			}
		})
	}
}

func TestExecutionImageAcceptsFullSHA256ImageIDs(t *testing.T) {
	t.Parallel()
	for _, image := range []string{"sha256:" + strings.Repeat("0", 64), "sha256:" + strings.Repeat("f", 64)} {
		if err := validateExecutionImage(image); err != nil {
			t.Fatalf("full image ID %q was refused: %v", image, err)
		}
	}
}

func TestLoadDefaultExecutionPolicyReadsTheAuthoritativeConfiguration(t *testing.T) {
	// Not parallel: it points the user configuration path at a temporary root.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "wb", "wb.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadDefaultExecutionPolicy()
	if err != nil {
		t.Fatalf("load configured policy: %v", err)
	}
	if policy == nil || *policy != executionTestPolicy() {
		t.Fatalf("policy = %#v", policy)
	}
	// A missing configuration is not an error and declares no policy.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	policy, err = LoadDefaultExecutionPolicy()
	if err != nil || policy != nil {
		t.Fatalf("missing configuration = %#v, %v", policy, err)
	}
	// A broken configuration must refuse, never read as "no policy".
	if err := os.WriteFile(path, []byte("agents: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDefaultExecutionPolicy(); err == nil {
		t.Fatal("a malformed configuration must not be treated as no policy")
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
		"leading dash repository": {
			body: executionYAML(mode, "-ghcr.io/acme/agent@sha256:"+strings.Repeat("e", 64), 8589934592, 8589934592, 2, 128, 1),
			want: "invalid registry",
		},
		"multiple digest separators": {
			body: executionYAML(mode, "ghcr.io/acme/agent@sha256:"+strings.Repeat("e", 64)+"@sha256:"+strings.Repeat("f", 64), 8589934592, 8589934592, 2, 128, 1),
			want: "at most one @ digest separator",
		},
		"control character in repository": {
			body: executionYAML(mode, "ghcr.io/acme/agent\x01@sha256:"+strings.Repeat("e", 64), 8589934592, 8589934592, 2, 128, 1),
			want: "printable ASCII",
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
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1) + `  providers:
    deepseek-official:
      base_url: https://api.deepseek.com
      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY
      wire_api: chat
  profiles:
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

func TestDispatchRefusesAnUnsupportedHarnessUnderAConfiguredMandatorySandbox(t *testing.T) {
	// Not parallel: the dispatch fixture pins environment variables.
	fixture := newDispatchFixture(t)
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1) + `  profiles:
    cheap: { harness: codex, provider: deepseek, model: deepseek-flash }
`
	fixture.deps.LoadConfig = func() (Config, error) {
		return LoadConfigFile(writeConfig(t, body))
	}
	// These seams are host side effects: a contained plan must refuse before
	// any of them can run.
	probes, versions := 0, 0
	fixture.deps.Preflight = func(context.Context, Resolved) error { probes++; return nil }
	fixture.deps.DetectVersion = func(context.Context, string) string { versions++; return "" }
	_, err := fixture.dispatch(t, newRequest())
	if err == nil {
		t.Fatal("a mandatory sandbox this build cannot enforce must refuse the dispatch")
	}
	if !strings.Contains(err.Error(), "can execute only the") || !strings.Contains(err.Error(), HarnessDSH) {
		t.Fatalf("error = %v, want the unsupported-harness refusal naming %q", err, HarnessDSH)
	}
	if probes != 0 || versions != 0 {
		t.Fatalf("a refused dispatch probed the host: preflight=%d detect-version=%d", probes, versions)
	}
	if fixture.createCalls != 0 || fixture.beforeCalls != 0 || fixture.listCalls != 0 || fixture.originCalls != 0 || fixture.spawnedAgentID != "" {
		t.Fatalf("a refused dispatch started side effects: create=%d before=%d list=%d origin=%d spawn=%q",
			fixture.createCalls, fixture.beforeCalls, fixture.listCalls, fixture.originCalls, fixture.spawnedAgentID)
	}
	records, err := NewStore(fixture.home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("a refused dispatch persisted a run: %#v", records)
	}
}

func TestDispatchAdmitsMatchingContainedDshWithoutHostProbes(t *testing.T) {
	// Not parallel: the dispatch fixture pins environment variables.
	fixture := newDispatchFixture(t)
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1) + `  providers:
    deepseek-official:
      base_url: https://api.deepseek.com
      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY
      wire_api: chat
  profiles:
    ds: { harness: dsh, model: deepseek-flash }
`
	fixture.deps.LoadConfig = func() (Config, error) {
		return LoadConfigFile(writeConfig(t, body))
	}
	fixture.deps.CreateWorktree = func(_ context.Context, _ []string, _ worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		fixture.createCalls++
		return []worktrees.CreateResult{{
			Repository: "acme/app", CanonicalDir: "/fleet/acme/app",
			WorktreeDir: "/fleet/.worktrees/task-one", Branch: "task-one",
			Base: "main", BaseSHA: "abc123", Action: "created",
		}}, nil
	}
	probes, versions := 0, 0
	fixture.deps.Preflight = func(context.Context, Resolved) error { probes++; return nil }
	fixture.deps.DetectVersion = func(context.Context, string) string { versions++; return "host-version" }
	request := newRequest()
	request.Profile = "ds"
	record, err := fixture.dispatch(t, request)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if probes != 0 || versions != 0 {
		t.Fatalf("a contained dispatch reached a host probe: preflight=%d detect-version=%d", probes, versions)
	}
	if fixture.createCalls != 1 || fixture.spawnedAgentID == "" {
		t.Fatalf("a contained dispatch did not admit and start: create=%d spawn=%q", fixture.createCalls, fixture.spawnedAgentID)
	}
	if record.Execution == nil || record.ExecutionWorkspace == nil || record.ExecutionWorkspace.InputDir != "" {
		t.Fatalf("contained admission = %#v / %#v", record.Execution, record.ExecutionWorkspace)
	}
	if record.ExecutionWorkspace.WorktreeDir != "/fleet/.worktrees/task-one" ||
		record.ExecutionWorkspace.GitCommonDir != "/fleet/acme/app/.git" {
		t.Fatalf("trusted workspace = %#v", record.ExecutionWorkspace)
	}
	if record.HarnessCapabilities.Version != "" {
		t.Fatalf("a contained dispatch recorded a host harness version %q", record.HarnessCapabilities.Version)
	}
	// A contained dsh run injects its one credential and isolates the worker in
	// the container; the record must not keep the adapter's host claim.
	if record.HarnessCapabilities.Credential != CredentialWBInjected {
		t.Fatalf("credential capability = %q, want %q", record.HarnessCapabilities.Credential, CredentialWBInjected)
	}
	if record.HarnessCapabilities.Isolation != IsolationLinuxContainer {
		t.Fatalf("isolation capability = %q, want %q", record.HarnessCapabilities.Isolation, IsolationLinuxContainer)
	}
}

func TestDispatchKeepsLegacyDshCapabilitiesWithoutAMandatoryPolicy(t *testing.T) {
	// Not parallel: the dispatch fixture pins environment variables.
	fixture := newDispatchFixture(t)
	body := `agents:
  providers:
    deepseek-official:
      base_url: https://api.deepseek.com
      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY
      wire_api: chat
  profiles:
    ds: { harness: dsh, model: deepseek-flash }
`
	fixture.deps.LoadConfig = func() (Config, error) {
		return LoadConfigFile(writeConfig(t, body))
	}
	request := newRequest()
	request.Profile = "ds"
	record, err := fixture.dispatch(t, request)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if record.Execution != nil {
		t.Fatalf("a host dispatch grew a mandatory policy: %#v", record.Execution)
	}
	if record.HarnessCapabilities.Credential != CredentialHarnessStore {
		t.Fatalf("credential capability = %q, want the legacy %q", record.HarnessCapabilities.Credential, CredentialHarnessStore)
	}
	if record.HarnessCapabilities.Isolation != IsolationUserHome {
		t.Fatalf("isolation capability = %q, want the legacy %q", record.HarnessCapabilities.Isolation, IsolationUserHome)
	}
}

func TestDispatchRefusesAContainedLifetimeBeyondTheMaximumBeforeCreatingAWorktree(t *testing.T) {
	// Not parallel: the dispatch fixture pins environment variables.
	fixture := newDispatchFixture(t)
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1) + `  providers:
    deepseek-official:
      base_url: https://api.deepseek.com
      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY
      wire_api: chat
  profiles:
    ds: { harness: dsh, model: deepseek-flash }
`
	fixture.deps.LoadConfig = func() (Config, error) {
		return LoadConfigFile(writeConfig(t, body))
	}
	request := newRequest()
	request.Profile = "ds"
	request.Timeout = maxContainerLifetime + time.Hour
	_, err := fixture.dispatch(t, request)
	if err == nil || !strings.Contains(err.Error(), "contained execution") {
		t.Fatalf("error = %v, want the contained lifetime refusal", err)
	}
	if fixture.createCalls != 0 || fixture.spawnedAgentID != "" {
		t.Fatalf("an over-long contained run created side effects: create=%d spawn=%q", fixture.createCalls, fixture.spawnedAgentID)
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
			probes, versions := 0, 0
			fixture.deps.Preflight = func(context.Context, Resolved) error { probes++; return nil }
			fixture.deps.DetectVersion = func(context.Context, string) string { versions++; return "" }
			plan := Plan{Profile: Resolved{Profile: "cheap", Harness: HarnessCodex, Provider: "deepseek", Model: "deepseek-flash"}, Execution: testCase.planned}
			_, err := fixture.dispatch(t, DispatchRequest{
				Mode: ModeNew, Worktree: "task-one", Task: "x", Timeout: time.Minute, Plan: &plan,
			})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
			if probes != 0 || versions != 0 {
				t.Fatalf("a refused dispatch probed the host: preflight=%d detect-version=%d", probes, versions)
			}
			if fixture.createCalls != 0 || fixture.spawnedAgentID != "" {
				t.Fatalf("a refused dispatch started side effects: create=%d spawn=%q", fixture.createCalls, fixture.spawnedAgentID)
			}
		})
	}
}
