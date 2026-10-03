package mergepolicy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

var errBoomPR9 = errors.New("pr9 boom")

func TestApplyClassicProtectionWithoutLinearHistoryInjectedHonoursInjectedFailures(t *testing.T) {
	service := New()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			err := service.applyClassicProtectionWithoutLinearHistoryInjected(context.Background(), "repos/acme/app/branches/main/protection", []byte("{}"), inj)
			if !errors.Is(err, errBoomPR9) {
				t.Fatalf("applyClassicProtectionWithoutLinearHistoryInjected(%s failure) = %v, want errBoomPR9", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(os.TempDir(), "wb-merge-policy-protection-*.json"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover scratch file(s) after an injected %s failure: %v", step, matches)
			}
		})
	}
}
func TestApplySharedRulesetInjectedHonoursInjectedFailures(t *testing.T) {
	service := New()
	service.deps.Read = func(context.Context, string) ([]byte, error) {
		return []byte(`{"id":7,"name":"default","target":"branch","enforcement":"active","rules":[{"type":"required_linear_history"}]}`), nil
	}
	service.deps.Execute = func(context.Context, ...string) githubobserver.CommandResponse {
		t.Fatal("mergePolicyExecute must not run when the scratch write itself fails")
		return githubobserver.CommandResponse{}
	}
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
		err := service.applySharedRulesetInjected(context.Background(), RulesetChange{SourceType: "Repository", Source: "acme/app", ID: 7}, inj)
		if !errors.Is(err, errBoomPR9) {
			t.Fatalf("applySharedRulesetInjected(%s failure) = %v, want errBoomPR9", step, err)
		}
		matches, globErr := filepath.Glob(filepath.Join(os.TempDir(), "wb-merge-policy-ruleset-*.json"))
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("leftover scratch file(s) after an injected %s failure: %v", step, matches)
		}
	}
}
