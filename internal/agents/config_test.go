package agents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHelpersExposeTheClosedVocabularies(t *testing.T) {
	t.Parallel()
	if got := AllHarnesses(); len(got) != 3 || got[0] != HarnessCodex {
		t.Fatalf("harnesses = %#v", got)
	}
	if err := NewRequestError("refused"); !IsRequestError(err) || err.Error() != "refused" {
		t.Fatalf("request error = %v", err)
	}
	config := plannerConfig(t)
	if names := config.TaskTypesInOrder(); len(names) < 2 || names[0] > names[1] {
		t.Fatalf("task types = %#v", names)
	}
	if names := config.ProfileNames(); len(names) != 4 || names[0] != "cheap" {
		t.Fatalf("profiles = %#v", names)
	}
	if names := config.ProviderNames(); len(names) < 2 {
		t.Fatalf("providers = %#v", names)
	}
	if names := (Config{}).validationNames(); names != "(none configured)" {
		t.Fatalf("validation names = %q", names)
	}
	if _, err := parseStepTimeout("-5m"); err == nil {
		t.Fatal("a negative duration must be refused")
	}
	if duration, err := parseStepTimeout(" 2m "); err != nil || duration != 2*time.Minute {
		t.Fatalf("duration = %v %v", duration, err)
	}
}

func TestLoadConfigFileRefusesEveryInvalidReference(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		body string
		want string
	}{
		"unreadable":              {body: "", want: ""},
		"invalid yaml":            {body: "agents: [not, a, mapping]", want: "parse agent configuration"},
		"empty suite":             {body: "agents:\n  validations:\n    \"\": []\n", want: "suite name must not be empty"},
		"no steps":                {body: "agents:\n  validations:\n    go: []\n", want: "has no steps"},
		"step no name":            {body: "agents:\n  validations:\n    go:\n      - { argv: [go, test] }\n", want: "requires a name"},
		"step no argv":            {body: "agents:\n  validations:\n    go:\n      - { name: vet }\n", want: "requires argv"},
		"step timeout":            {body: "agents:\n  validations:\n    go:\n      - { name: vet, argv: [go], timeout: soon }\n", want: "is not a Go duration"},
		"unknown profile":         {body: "agents:\n  task_types:\n    coding:\n      profile: nope\n", want: "names unknown profile"},
		"bad complexity":          {body: "agents:\n  task_types:\n    coding:\n      complexity: { huge: coding }\n", want: "complexity \"huge\" is not one of"},
		"complexity profile":      {body: "agents:\n  task_types:\n    coding:\n      complexity: { high: nope }\n", want: "names unknown profile"},
		"bad effort":              {body: "agents:\n  task_types:\n    coding:\n      effort: maximum\n", want: "effort \"maximum\" is not one of"},
		"bad worktree":            {body: "agents:\n  task_types:\n    coding:\n      worktree: somewhere\n", want: "worktree \"somewhere\" must be"},
		"bad tt timeout":          {body: "agents:\n  task_types:\n    coding:\n      timeout: soon\n", want: "is not a Go duration"},
		"unknown suite":           {body: "agents:\n  task_types:\n    coding:\n      validate: [nope]\n", want: "names unknown validation suite"},
		"bad acceptance":          {body: "agents:\n  task_types:\n    coding:\n      acceptance: maybe\n", want: "acceptance \"maybe\" must be"},
		"default tt":              {body: "agents:\n  defaults:\n    task_type: nope\n", want: "is not a configured task type"},
		"default profile":         {body: "agents:\n  defaults:\n    profile: nope\n", want: "is not a configured profile"},
		"unusable fallback":       {body: "agents:\n  fallback: [nope]\n", want: "is not usable"},
		"both credential sources": {body: "agents:\n  providers:\n    p: { base_url: https://x, credential_env: A, credential_file: /b }\n", want: "exactly one credential source"},
		"bad provider url":        {body: "agents:\n  providers:\n    p: { base_url: http://x, credential_env: A }\n", want: "must be an https URL"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var path string
			switch name {
			case "unreadable":
				path = t.TempDir() // a directory cannot be read as a file
			case "invalid yaml":
				path = writeConfig(t, testCase.body)
			default:
				path = writeConfig(t, testCase.body)
			}
			_, err := LoadConfigFile(path)
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if testCase.want != "" && !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestResolveRefusesProfileShapesItCannotExecute(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		harness string
		fields  string
		want    string
	}{
		"both effort and reasoning":  {"codex", "provider: deepseek\n      model: m\n      effort: high\n      reasoning: high\n", "both effort and the legacy reasoning"},
		"bad effort":                 {"codex", "provider: deepseek\n      model: m\n      effort: maximum\n", "is not one of"},
		"variant on codex":           {"codex", "provider: deepseek\n      model: m\n      variant: fast\n", "only the \"opencode\" harness supports"},
		"bad variant":                {"opencode", "model: p/m\n      variant: has space\n", "non-secret execution identifier"},
		"bad effort variant key":     {"opencode", "model: p/m\n      effort_variants: { maximum: x }\n", "effort_variants key"},
		"empty effort variant":       {"opencode", "model: p/m\n      effort_variants: { high: \"\" }\n", "effort_variants \"high\" is empty"},
		"bad effort variant value":   {"opencode", "model: p/m\n      effort_variants: { high: \"two words\" }\n", "non-secret execution identifier"},
		"effort variants on codex":   {"codex", "provider: deepseek\n      model: m\n      effort_variants: { high: x }\n", "only the \"opencode\" harness supports"},
		"bad profile timeout":        {"codex", "provider: deepseek\n      model: m\n      timeout: soon\n", "is not a Go duration"},
		"opencode model shape":       {"opencode", "model: no-slash\n", "provider/model"},
		"opencode provider mismatch": {"opencode", "model: a/m\n      provider: b\n", "belongs to provider"},
		"dsh provider":               {"dsh", "model: m\n      provider: openrouter\n", "supports only"},
		"codex unknown provider":     {"codex", "provider: nope\n      model: m\n", "unknown provider"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := "agents:\n  profiles:\n    p:\n      harness: " + testCase.harness + "\n      " + testCase.fields
			config, err := LoadConfigFile(writeConfig(t, body))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			_, err = config.Resolve("p")
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestResolveAcceptsProviderMetadataAndAProfileTimeout(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    op:
      harness: opencode
      model: opencode-go/m
      provider: opencode-go
      timeout: 90m
      effort_variants: { high: heavy }
    ds:
      harness: dsh
      model: deepseek-v4-flash
      provider: deepseek-official
`))
	if err != nil {
		t.Fatal(err)
	}
	op, err := config.Resolve("op")
	if err != nil {
		t.Fatalf("opencode profile: %v", err)
	}
	if op.Provider != "opencode-go" || op.TimeoutMS != 5400000 || op.EffortVariants["high"] != "heavy" {
		t.Fatalf("resolved = %#v", op)
	}
	ds, err := config.Resolve("ds")
	if err != nil {
		t.Fatalf("dsh profile: %v", err)
	}
	if ds.Provider != ProviderDshDefault {
		t.Fatalf("dsh provider = %q", ds.Provider)
	}
}

func TestConfigValidationSurfacesEveryTaskTypeShape(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    p: { harness: codex, provider: deepseek, model: m }
  validations:
    go: [{ name: vet, argv: [go, vet] }]
  task_types:
    ok:
      profile: p
      effort: high
      worktree: current
      timeout: 30m
      validate: [go]
      acceptance: optional
`))
	if err != nil {
		t.Fatalf("a complete task type must load: %v", err)
	}
	if err := config.validateTaskType("", TaskType{}); err != nil {
		t.Fatalf("an empty task type is valid policy: %v", err)
	}
}

func TestConfigReadsANonDirectoryConfigurationPath(t *testing.T) {
	t.Parallel()
	// A path that exists but cannot be read as a file (a directory) is a real
	// read failure, not "missing configuration".
	if _, err := LoadConfigFile(t.TempDir()); err == nil {
		t.Fatal("a directory must be reported as unreadable configuration")
	}
	if _, err := LoadConfigFile(filepath.Join(t.TempDir(), "wb.yaml")); err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackReportsWhyEveryCandidateFailed(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	config.Fallback = []string{"missing", "cheap"}
	planner := Planner{Config: config, Probe: func(resolved Resolved) error {
		return errors.New("unavailable " + resolved.Profile)
	}}
	_, err := planner.Plan(RunRequest{Task: "x", TaskType: "coding"})
	if err == nil || !strings.Contains(err.Error(), "no configured fallback is usable") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("fallback failure = %v", err)
	}
}

func TestPlanDefaultsTheWorktreeModeWhenTheTaskTypeNamesNone(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	taskType := config.TaskTypes["coding"]
	taskType.Worktree = ""
	config.TaskTypes["coding"] = taskType
	plan, err := (Planner{Config: config}).Plan(RunRequest{Task: "x", TaskType: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.WorktreeMode != ModeAuto || plan.WorktreeSource != SourceDefault {
		t.Fatalf("worktree = %q/%q", plan.WorktreeMode, plan.WorktreeSource)
	}
}

func TestPlanHonoursExplicitValidationAndRequiredAcceptance(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	plan, err := (Planner{Config: config}).Plan(RunRequest{
		Task: "x", Profile: "coder", Validate: []string{"go"}, ValidateSet: true, Acceptance: AcceptanceRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ValidationSource != SourceExplicit || len(plan.Validation) != 1 {
		t.Fatalf("validation = %#v/%q", plan.Validation, plan.ValidationSource)
	}
	if !plan.AcceptanceRequired {
		t.Fatal("an explicit required acceptance must win")
	}
}

func TestPlanFallsBackWhenTheProbeRejectsTheRoutedProfile(t *testing.T) {
	t.Parallel()
	config := plannerConfig(t)
	plan, err := (Planner{Config: config, Probe: func(resolved Resolved) error {
		if resolved.Profile == "coder" {
			return errors.New("missing")
		}
		return nil
	}}).Plan(RunRequest{Task: "x", TaskType: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.EffortSource != SourceNone || plan.EffortApplied {
		t.Fatalf("the fallback profile's own effort must be re-resolved: %#v", plan)
	}
}

func TestSubtractPathsKeepsEverythingWhenNothingWasDirtyBefore(t *testing.T) {
	t.Parallel()
	paths := []string{"a", "b"}
	if got := subtractPaths(paths, nil); len(got) != 2 {
		t.Fatalf("paths = %#v", got)
	}
	if got := subtractPaths(paths, []string{"a"}); len(got) != 1 || got[0] != "b" {
		t.Fatalf("filtered paths = %#v", got)
	}
}

func TestPlanRefusesAFallbackThatCannotHonourAnExplicitEffort(t *testing.T) {
	t.Parallel()
	config, err := LoadConfigFile(writeConfig(t, `
agents:
  profiles:
    coder: { harness: codex, provider: deepseek, model: m }
    ds: { harness: dsh, model: deepseek-v4-flash }
  task_types:
    coding: { profile: coder }
  fallback: [ds]
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Planner{Config: config, Probe: func(resolved Resolved) error {
		if resolved.Profile == "coder" {
			return errors.New("unavailable")
		}
		return nil
	}}).Plan(RunRequest{Task: "x", TaskType: "coding", Effort: "high"})
	if err == nil || !IsRequestError(err) || !strings.Contains(err.Error(), "cannot be applied") {
		t.Fatalf("explicit effort on a fallback = %v", err)
	}
}
