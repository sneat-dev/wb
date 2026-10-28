package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func containedDshTestDirs(t *testing.T) (worktree, gitCommon, runDir string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test temp root: %v", err)
	}
	worktree = filepath.Join(root, "worktree")
	gitCommon = filepath.Join(root, "git-common")
	runDir = filepath.Join(root, "run")
	for _, directory := range []string{worktree, gitCommon, runDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create test directory %s: %v", directory, err)
		}
	}
	return worktree, gitCommon, runDir
}

func containedDshTestRecord(t *testing.T, worktree, gitCommon string) Record {
	t.Helper()
	return Record{
		AgentID: "agt-" + strings.Repeat("a", 32),
		Resolved: Resolved{
			Harness:  HarnessDSH,
			Provider: ProviderDshDefault,
			Model:    "deepseek-v4-flash",
			Routing: Provider{
				BaseURL:       "https://api.deepseek.com/v1",
				CredentialEnv: dshMandatoryProviderEnv,
				WireAPI:       WireAPIChat,
			},
		},
		WorktreeDir: worktree,
		ExecutionWorkspace: &ContainerWorkspace{
			WorktreeDir:  worktree,
			GitCommonDir: gitCommon,
		},
		Task: "fix the failing test",
	}
}

func containedDshTestFields(worktree, gitCommon, runDir string) ContainedDshLaunchFields {
	return ContainedDshLaunchFields{
		RunDir:            runDir,
		WorktreeDir:       worktree,
		GitCommonDir:      gitCommon,
		Provider:          ProviderDshDefault,
		Harness:           HarnessDSH,
		Model:             "deepseek-v4-flash",
		Task:              "fix the failing test",
		BaseURL:           "https://api.deepseek.com",
		CredentialEnvName: dshMandatoryProviderEnv,
	}
}

func TestContainedDshLaunchPreparesOnlyItsScopedInputDirectory(t *testing.T) {
	const secret = "credential-value-must-never-be-written"
	t.Setenv(dshMandatoryProviderEnv, secret)
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	record := containedDshTestRecord(t, worktree, gitCommon)

	prepared, err := PrepareContainedDshLaunchFromRecord(record, runDir, dshMandatoryProviderEnv)
	if err != nil {
		t.Fatalf("PrepareContainedDshLaunchFromRecord: %v", err)
	}
	inputDir := filepath.Join(runDir, containedDshInputDirName)
	if prepared.Workspace == nil {
		t.Fatal("no workspace was returned")
	}
	if prepared.Workspace.WorktreeDir != worktree || prepared.Workspace.GitCommonDir != gitCommon {
		t.Fatalf("workspace = %#v", prepared.Workspace)
	}
	if prepared.Workspace.InputDir != inputDir {
		t.Fatalf("input directory = %q, want the fixed scoped directory %q", prepared.Workspace.InputDir, inputDir)
	}
	if prepared.Workspace == record.ExecutionWorkspace {
		t.Fatal("the prepared workspace must be a snapshot, not the record's pointer")
	}

	entries, err := os.ReadDir(inputDir)
	if err != nil {
		t.Fatalf("read scoped input directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != dshModelPatchName {
		t.Fatalf("scoped input directory entries = %#v, want only %q", entries, dshModelPatchName)
	}
	info, err := os.Stat(inputDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("input directory mode = %04o, want 0700", info.Mode().Perm())
	}
	patchPath := filepath.Join(inputDir, dshModelPatchName)
	patchInfo, err := os.Stat(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if patchInfo.Mode().Perm() != 0o600 {
		t.Fatalf("overlay mode = %04o, want 0600", patchInfo.Mode().Perm())
	}

	wantArgv := []string{"--profile", dshProfile, "--patch", "/wb-input/" + dshModelPatchName, "fix the failing test"}
	if !reflect.DeepEqual(prepared.Launch.Argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", prepared.Launch.Argv, wantArgv)
	}
	if !reflect.DeepEqual(prepared.Launch.PrivateFiles, []string{patchPath}) {
		t.Fatalf("private files = %#v, want the real host overlay %q", prepared.Launch.PrivateFiles, patchPath)
	}
	if prepared.Launch.Env != nil || prepared.Launch.Stdin != "" || !prepared.Launch.SplitStderr {
		t.Fatalf("launch = %#v, want no host environment or stdin and split stderr", prepared.Launch)
	}

	raw, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("overlay contains the credential value: %s", raw)
	}
	for _, argument := range prepared.Launch.Argv {
		if strings.Contains(argument, secret) {
			t.Fatalf("argv contains the credential value: %#v", prepared.Launch.Argv)
		}
	}
	var overlay []containedDshPatchEntry
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		t.Fatalf("parse overlay: %v\n%s", err, raw)
	}
	if len(overlay) != 2 {
		t.Fatalf("overlay entries = %#v, want exactly two", overlay)
	}
	if overlay[0].ID != "agent-default-model" ||
		overlay[0].Config.Provider != ProviderDshDefault ||
		overlay[0].Config.Model != "deepseek-v4-flash" {
		t.Fatalf("model entry = %#v", overlay[0])
	}
	if overlay[0].Config.APIKeyEnv != "" || overlay[0].Config.BaseURL != "" {
		t.Fatalf("the model entry must not carry provider transport config: %#v", overlay[0])
	}
	if overlay[1].ID != "llm-deepseek" ||
		overlay[1].Config.APIKeyEnv != dshMandatoryProviderEnv ||
		overlay[1].Config.BaseURL != "https://api.deepseek.com/v1" ||
		overlay[1].Config.ReasoningEffort != "low" ||
		overlay[1].Config.Thinking != "enabled" ||
		overlay[1].Config.MaxTokens != 32768 {
		t.Fatalf("llm entry = %#v", overlay[1])
	}
	if overlay[1].Config.Provider != "" || overlay[1].Config.Model != "" {
		t.Fatalf("the llm entry must not repeat provider/model identity: %#v", overlay[1])
	}

	// The returned workspace is a snapshot: a later mutation must not rewrite
	// what the record says was trusted.
	prepared.Workspace.WorktreeDir = "/mutated"
	if record.ExecutionWorkspace.WorktreeDir != worktree {
		t.Fatal("mutating the prepared workspace rewrote the record's workspace")
	}
}

func TestContainedDshLaunchUsesTheSharedTaskNormalizationAndOfficialDefaultBase(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	fields := containedDshTestFields(worktree, gitCommon, runDir)
	fields.Task = "-h fix it"
	fields.BaseURL = ""
	prepared, err := PrepareContainedDshLaunch(fields)
	if err != nil {
		t.Fatalf("PrepareContainedDshLaunch: %v", err)
	}
	if got := prepared.Launch.Argv[len(prepared.Launch.Argv)-1]; got != "\n-h fix it" {
		t.Fatalf("normalized task argument = %q", got)
	}
	if !dshTaskNormalized(fields.Task) {
		t.Fatal("an option-shaped task must still be reported as normalized")
	}
	raw, err := os.ReadFile(prepared.Launch.PrivateFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	var overlay []containedDshPatchEntry
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		t.Fatalf("parse overlay: %v\n%s", err, raw)
	}
	if overlay[1].Config.BaseURL != containedDshDefaultBaseURL {
		t.Fatalf("default base URL = %q, want %q", overlay[1].Config.BaseURL, containedDshDefaultBaseURL)
	}
}

func TestContainedDshLaunchRefusesWrongOrMalformedIdentity(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	notDir := filepath.Join(runDir, "not-a-directory")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := containedDshTestFields(worktree, gitCommon, runDir)
	cases := map[string]struct {
		mutate func(*ContainedDshLaunchFields)
		want   string
	}{
		"provider alias": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.Provider = "deepseek" },
			want:   "only official provider",
		},
		"wrong harness": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.Harness = HarnessCodex },
			want:   "harness",
		},
		"blank model": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.Model = " " },
			want:   "model",
		},
		"model with surrounding whitespace": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.Model = " deepseek-v4-flash" },
			want:   "model",
		},
		"model with unsupported characters": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.Model = "deepseek v4" },
			want:   "model",
		},
		"empty credential name": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.CredentialEnvName = "" },
			want:   "credential environment name is required",
		},
		"credential name with whitespace": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.CredentialEnvName = " " + dshMandatoryProviderEnv },
			want:   "surrounding whitespace",
		},
		"lowercase credential name": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.CredentialEnvName = "deepseek_key" },
			want:   "uppercase shell variable",
		},
		"reserved credential name": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.CredentialEnvName = "DOCKER_HOST" },
			want:   "reserved",
		},
		"unofficial base URL": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.BaseURL = "https://deepseek.example.com" },
			want:   "official HTTPS DeepSeek endpoint",
		},
		"empty run directory": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.RunDir = "" },
			want:   "is empty",
		},
		"relative run directory": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.RunDir = "run" },
			want:   "not absolute",
		},
		"missing run directory": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.RunDir = filepath.Join(runDir, "absent") },
			want:   "inspect run directory",
		},
		"run target is a file": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.RunDir = notDir },
			want:   "not a directory",
		},
		"empty worktree": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.WorktreeDir = "" },
			want:   "is empty",
		},
		"relative worktree": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.WorktreeDir = "worktree" },
			want:   "not absolute",
		},
		"worktree with surrounding whitespace": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.WorktreeDir = " " + worktree },
			want:   "leading or trailing whitespace",
		},
		"unclean worktree": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.WorktreeDir = worktree + "/." },
			want:   "not a clean path",
		},
		"unsafe worktree character": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.WorktreeDir = worktree + ",x" },
			want:   "unsafe character",
		},
		"empty git common directory": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.GitCommonDir = "" },
			want:   "is empty",
		},
		"overlapping git common directory": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.GitCommonDir = worktree },
			want:   "must be disjoint",
		},
		"blank task": {
			mutate: func(fields *ContainedDshLaunchFields) { fields.Task = " " },
			want:   "non-empty task",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fields := base
			testCase.mutate(&fields)
			if _, err := PrepareContainedDshLaunch(fields); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestContainedDshLaunchRefusesUntrustedRecordWorkspace(t *testing.T) {
	t.Parallel()
	worktree, gitCommon, runDir := containedDshTestDirs(t)
	cases := map[string]struct {
		mutate func(*Record)
		want   string
	}{
		"absent workspace": {
			mutate: func(record *Record) { record.ExecutionWorkspace = nil },
			want:   "no trusted execution workspace",
		},
		"record-supplied input directory": {
			mutate: func(record *Record) { record.ExecutionWorkspace.InputDir = filepath.Join(runDir, "other") },
			want:   "record supplies input directory",
		},
		"workspace worktree mismatch": {
			mutate: func(record *Record) { record.ExecutionWorkspace.WorktreeDir = worktree + "-other" },
			want:   "does not match the record worktree",
		},
		"absent record worktree": {
			mutate: func(record *Record) { record.WorktreeDir = "" },
			want:   "no trusted worktree directory",
		},
		"routing credential mismatch": {
			mutate: func(record *Record) { record.Resolved.Routing.CredentialEnv = "OTHER_PROVIDER_KEY" },
			want:   "does not match the resolved routing name",
		},
		"routing without a credential source": {
			mutate: func(record *Record) { record.Resolved.Routing = Provider{} },
			want:   "credential source",
		},
		"wrong provider": {
			mutate: func(record *Record) { record.Resolved.Provider = "deepseek" },
			want:   "only official provider",
		},
		"wrong harness": {
			mutate: func(record *Record) { record.Resolved.Harness = HarnessCodex },
			want:   "harness",
		},
		"missing model": {
			mutate: func(record *Record) { record.Resolved.Model = "" },
			want:   "model",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			record := containedDshTestRecord(t, worktree, gitCommon)
			testCase.mutate(&record)
			if _, err := PrepareContainedDshLaunchFromRecord(record, runDir, dshMandatoryProviderEnv); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}

	// A file-sourced routing is injected under WB's fixed name; any other name
	// is refused rather than silently ignored.
	fileRecord := containedDshTestRecord(t, worktree, gitCommon)
	fileRecord.Resolved.Routing = Provider{
		BaseURL:        "https://api.deepseek.com",
		CredentialFile: filepath.Join(runDir, "provider.key"),
		WireAPI:        WireAPIChat,
	}
	if _, err := PrepareContainedDshLaunchFromRecord(fileRecord, runDir, dshMandatoryProviderEnv); err == nil || !strings.Contains(err.Error(), "file-sourced credential") {
		t.Fatalf("file routing error = %v", err)
	}
	prepared, err := PrepareContainedDshLaunchFromRecord(fileRecord, runDir, ProviderCredentialEnv)
	if err != nil {
		t.Fatalf("the fixed file-credential name must be accepted: %v", err)
	}
	if prepared.Workspace.InputDir == "" {
		t.Fatal("a successful file-sourced preparation must still create the scoped input directory")
	}
}

func TestContainedDshLaunchRefusesASymlinkedInputOrRunDirectory(t *testing.T) {
	t.Parallel()
	t.Run("input directory symlink", func(t *testing.T) {
		t.Parallel()
		worktree, gitCommon, runDir := containedDshTestDirs(t)
		outside := filepath.Join(filepath.Dir(runDir), "outside")
		if err := os.MkdirAll(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(runDir, containedDshInputDirName)); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir)); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error = %v, want a symlink refusal", err)
		}
	})
	t.Run("run directory symlink", func(t *testing.T) {
		t.Parallel()
		worktree, gitCommon, runDir := containedDshTestDirs(t)
		link := filepath.Join(filepath.Dir(runDir), "run-link")
		if err := os.Symlink(runDir, link); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, link)); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error = %v, want a symlink refusal", err)
		}
	})
	t.Run("existing non-directory input target", func(t *testing.T) {
		t.Parallel()
		worktree, gitCommon, runDir := containedDshTestDirs(t)
		if err := os.WriteFile(filepath.Join(runDir, containedDshInputDirName), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir)); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("error = %v, want an existing-path refusal", err)
		}
	})
	t.Run("input directory overlaps the worktree", func(t *testing.T) {
		t.Parallel()
		worktree, gitCommon, _ := containedDshTestDirs(t)
		runDir := filepath.Join(worktree, "run")
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, runDir)); err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("error = %v, want an input-overlap refusal", err)
		}
	})
	t.Run("run directory path contains a symlink", func(t *testing.T) {
		t.Parallel()
		worktree, gitCommon, runDir := containedDshTestDirs(t)
		actual := filepath.Join(filepath.Dir(runDir), "actual")
		if err := os.MkdirAll(actual, 0o700); err != nil {
			t.Fatal(err)
		}
		linkRoot := filepath.Join(filepath.Dir(runDir), "link-root")
		if err := os.Symlink(actual, linkRoot); err != nil {
			t.Fatal(err)
		}
		linkedRun := filepath.Join(linkRoot, "run")
		if err := os.MkdirAll(filepath.Join(actual, "run"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareContainedDshLaunch(containedDshTestFields(worktree, gitCommon, linkedRun)); err == nil || !strings.Contains(err.Error(), "contains a symlink") {
			t.Fatalf("error = %v, want a symlinked-path refusal", err)
		}
	})
}

func TestLegacyDshPrepareStillWritesTheSingleModelPatch(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	launch, err := (dshHarness{}).Prepare(Launch{
		RunDir: runDir, WorktreeDir: "/work", Model: "deepseek-v4-flash", Task: "run the tests",
	})
	if err != nil {
		t.Fatalf("legacy Prepare: %v", err)
	}
	patchPath := filepath.Join(runDir, dshModelPatchName)
	if len(launch.PrivateFiles) != 1 || launch.PrivateFiles[0] != patchPath {
		t.Fatalf("legacy private files = %#v, want %q", launch.PrivateFiles, patchPath)
	}
	if launch.Argv[3] != patchPath {
		t.Fatalf("legacy patch argument = %q, want the run-directory path %q", launch.Argv[3], patchPath)
	}
	raw, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"llm-deepseek", "apiKeyEnv", "reasoningEffort", "thinking", "maxTokens"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("legacy host overlay must stay unchanged; it contains %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{"- id: agent-default-model", "provider: " + ProviderDshDefault, "model: deepseek-v4-flash"} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("legacy overlay is missing %q: %s", required, raw)
		}
	}
}
