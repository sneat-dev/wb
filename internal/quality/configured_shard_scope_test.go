package quality

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func configuredShardScopeFixture(t *testing.T, packages []string) (string, string, RunOptions) {
	t.Helper()
	module := t.TempDir()
	log := filepath.Join(module, "runs.log")
	writeCoverageFixture(t, filepath.Join(module, "go.mod"), "module example.test/configscope\n\ngo 1.24\n")
	for _, name := range []string{"selected", "excluded"} {
		tests := fmt.Sprintf(`package %s
import("os";"testing")
func TestNative(t *testing.T){if Covered()!=1{t.Fatal("value")};f,e:=os.OpenFile(%q,os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if e!=nil{t.Fatal(e)};defer f.Close();if _,e=f.WriteString(%q);e!=nil{t.Fatal(e)}}
`, name, log, name+"\n")
		writeGoShardFixturePackage(t, module, name, fmt.Sprintf("package %s\nfunc Covered()int{return 1}\n", name), tests)
	}
	if err := os.MkdirAll(filepath.Join(module, ".wb"), 0700); err != nil {
		t.Fatal(err)
	}
	writeCoverageFixture(t, filepath.Join(module, repositoryQualityConfigPath), "version: 1\ngo_test:\n  shards: 2\n  packages: ["+strings.Join(packages, ", ")+"]\n")
	options, err := RepositoryRunOptions(module, RunOptions{GoTestPackages: []string{"./selected"}})
	if err != nil {
		t.Fatal(err)
	}
	if !options.configGoShardPackages {
		t.Fatal("validated policy did not retain provenance")
	}
	return module, log, options
}

func TestConfiguredShardsRespectActualSelectedPackageScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		shards   []string
		selected []string
	}{
		{"all outside use unsharded once", []string{"./excluded"}, []string{"./selected"}},
		{"eligible and outside", []string{"./selected", "./excluded"}, []string{"example.test/configscope/selected"}},
		{"selected pattern resolves canonically", []string{"./selected", "./excluded"}, []string{"./selected/..."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			module, log, options := configuredShardScopeFixture(t, tc.shards)
			options.GoTestPackages = tc.selected
			profile := filepath.Join(module, "selected.cov")
			output, _, err := runCoverageWithOptions(context.Background(), options, module, profile)
			if err != nil {
				t.Fatalf("selected actual run=%v output=%s", err, output)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			runs := strings.Fields(string(raw))
			sort.Strings(runs)
			if !reflect.DeepEqual(runs, []string{"selected"}) {
				t.Fatalf("actual packages/test counts=%v", runs)
			}
			raw, err = os.ReadFile(profile)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "/excluded/") || !strings.Contains(string(raw), "/selected/") {
				t.Fatalf("profile selected scope=%s", raw)
			}
		})
	}
}

func TestConfiguredShardsCopiedExplicitOverrideRemainsStrict(t *testing.T) {
	t.Parallel()
	module, log, options := configuredShardScopeFixture(t, []string{"./excluded"})
	options.ExplicitGoTestSharding = true
	_, _, err := runCoverageWithOptions(context.Background(), options, module, filepath.Join(module, "explicit.cov"))
	if err == nil || err.Error() != `shard package "./excluded" resolves outside selected package scope` {
		t.Fatalf("copied explicit error=%v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("explicit outside scope executed tests: %v", err)
	}
}

func TestConfiguredShardsValidateExcludedCanonicalRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		shards []string
		want   string
	}{
		{"duplicate aliases", []string{"./excluded", "example.test/configscope/excluded"}, `duplicate shard package "example.test/configscope/excluded"`},
		{"multiple packages", []string{"./..."}, `resolved to 2 packages; name exactly one package`},
		{"missing package", []string{"./absent"}, "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			module, log, options := configuredShardScopeFixture(t, tc.shards)
			_, _, err := runCoverageWithOptions(context.Background(), options, module, filepath.Join(module, "invalid.cov"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid configured error=%v want %q", err, tc.want)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("invalid policy executed tests: %v", err)
			}
		})
	}
}

func TestConfiguredShardsRetainActualPolicyLoadRefusals(t *testing.T) {
	t.Parallel()
	base := RunOptions{GoTestShards: 3, GoShardPackages: []string{"./unchanged"}}
	missing := t.TempDir()
	got, err := RepositoryRunOptions(missing, base)
	if err != nil || got.GoTestShards != base.GoTestShards || !reflect.DeepEqual(got.GoShardPackages, base.GoShardPackages) || got.configGoShardPackages {
		t.Fatalf("absent policy options=%+v error=%v", got, err)
	}
	blocked := t.TempDir()
	writeCoverageFixture(t, filepath.Join(blocked, ".wb"), "actual regular-file blocker")
	if _, err := RepositoryRunOptions(blocked, base); err == nil || !strings.Contains(err.Error(), "open repository quality policy") {
		t.Fatalf("actual open refusal=%v", err)
	}
	for _, tc := range []struct{ name, contents, want string }{
		{"trailing malformed YAML", "version: 1\n---\n{ broken", "decode trailing repository quality policy"},
		{"empty configured package", "version: 1\ngo_test:\n  shards: 2\n  packages: ['  ']\n", "contains an empty go_test package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".wb"), 0700); err != nil {
				t.Fatal(err)
			}
			writeCoverageFixture(t, filepath.Join(root, repositoryQualityConfigPath), tc.contents)
			if _, err := RepositoryRunOptions(root, base); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("actual policy refusal=%v want %q", err, tc.want)
			}
		})
	}
}

func TestConfiguredShardsCombinedTierKeepsInvalidScopeRefusal(t *testing.T) {
	t.Parallel()
	_, attempts, err := runCoverageWithOptions(context.Background(), RunOptions{IncludeE2E: true, GoTestPackages: []string{"-invalid"}}, t.TempDir(), filepath.Join(t.TempDir(), "never.cov"))
	if attempts != 0 || err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
		t.Fatalf("combined tier input error=%v attempts=%d", err, attempts)
	}
}
