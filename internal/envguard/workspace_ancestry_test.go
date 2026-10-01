package envguard

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGoWorkAncestorsResolutionFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("working directory unavailable")
	if got := goWorkAncestorsResolved("relative", func(string) (string, error) { return "", failure }); got != nil {
		t.Fatalf("unresolved ancestry = %v, want nil", got)
	}
}

func TestGoWorkAncestorsAndNearestAgree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, child} {
		if err := os.WriteFile(filepath.Join(dir, "go.work"), []byte("go 1.27\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := goWorkAncestors(child)
	if len(got) < 2 || got[0] != filepath.Join(child, "go.work") || got[1] != filepath.Join(root, "go.work") {
		t.Fatalf("ancestry order = %v", got)
	}
	if got := nearestGoWorkDir(child); got != child {
		t.Fatalf("nearest = %q, want %q", got, child)
	}
}

func TestSanitizeEnvAgentOverridesRemainUnique(t *testing.T) {
	t.Parallel()
	got := SanitizeEnv([]string{"WB_AGENT_ID=ambient", "A=first", "WB_AGENT_ID=ambient-again", "A=second"}, "WB_AGENT_ID=explicit", "A=final", "WB_AGENT_ID=final-agent")
	want := []string{"A=final", "WB_AGENT_ID=final-agent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
}
