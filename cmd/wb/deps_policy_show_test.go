package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testPolicyDocumentWithLayerForbid extends the shared layered policy
// fixture with an explicit layers.forbid edge that carries a reason, so
// `wb deps policy show` has something to print under "layers".
const testPolicyDocumentWithLayerForbid = `
groups:
  - {name: own-repo,                 match: ["<self>/..."]}
  - {name: extension-contract,       match: ["github.com/acme/ext-*/..."]}
  - {name: extension-implementation, match: ["github.com/acme/*/..."]}
  - {name: dalgo-adapter,            match: ["github.com/dal-go/dalgo{2,4}*/..."]}
  - {name: dalgo-core,               match: ["github.com/dal-go/..."]}
  - {name: third-party,              match: ["..."]}
types:
  - name: extension-contract
    detect: ["github.com/acme/ext-*/backend"]
    scopes: {source: {allow: [own-repo, extension-contract, third-party]}}
  - name: extension-implementation
    detect: ["github.com/acme/*/backend"]
    scopes:
      source: {allow: [own-repo, extension-contract, dalgo-core, third-party]}
      tests:  {allow: [own-repo, extension-contract, dalgo-core, dalgo-adapter, third-party]}
layers:
  mode: report
  roles: {api: ["api4*"], facade: ["facade4*"], dal: ["dal4*"]}
  order: [[api], [facade], [dal]]
  forbid:
    - {from: dal, to: api, reason: "data access must not call back into the api layer"}
expect:
  - {import: "github.com/acme/ext-x/backend/dto", group: extension-contract}
  - {module: "github.com/acme/cal/backend",       type: extension-implementation}
`

func writeRwi01TestPolicyWithLayerForbid(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(testPolicyDocumentWithLayerForbid), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDirectoryArgDefaultsToCurrentDirectory covers directoryArg's
// fallback branch: no args at all (nil), not just an empty first element.
func TestDirectoryArgDefaultsToCurrentDirectory(t *testing.T) {
	t.Parallel()
	if got := directoryArg(nil); got != "." {
		t.Fatalf("directoryArg(nil) = %q, want %q", got, ".")
	}
}

// TestPolicyShowPrintsConfigPathStrictAndLayerForbidReason drives `wb
// deps policy show` with a repository config (.wb-deps-policy.yaml) that
// sets strict: true, and a policy whose layers declare a forbidden edge
// with a reason: the show output must name the config file, report strict
// mode, and print the forbidden edge's reason, none of which the shared
// fixture (no config file, no layers.forbid) reaches.
func TestPolicyShowPrintsConfigPathStrictAndLayerForbidReason(t *testing.T) {
	root := violatingModule(t)
	policyPath := writeRwi01TestPolicyWithLayerForbid(t)
	configPath := filepath.Join(root, ".wb-deps-policy.yaml")
	configBody := "policy: " + policyPath + "\nstrict: true\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicy(t, "show", root)
	if err != nil {
		t.Fatalf("wb deps policy show with a strict config: %v\n%s", err, out)
	}
	for _, want := range []string{
		"config   " + configPath,
		"strict   on",
		"forbidden: dal -> api — data access must not call back into the api layer",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
}

// rwi01ExpectationFailurePolicy declares two assertions that both fail: one
// with a plain group mismatch (Err stays empty) and one whose module never
// matches any type's detect pattern (Err is set from the detection error).
// It drives every branch of `wb deps policy test` past a clean run: the
// failed++ counter, the Err-set detail line, the per-assertion FAIL line,
// and the closing "N assertion(s) failed" refusal.
const rwi01ExpectationFailurePolicy = `
groups:
  - {name: own-repo,    match: ["github.com/acme/own/..."]}
  - {name: third-party, match: ["..."]}
types:
  - name: service
    detect: ["github.com/acme/svc-x/backend"]
    scopes: {source: {allow: [own-repo, third-party]}}
expect:
  - {import: "github.com/acme/other/pkg",              group: own-repo}
  - {module: "github.com/acme/does-not-match/backend", type: service}
`

// TestPolicyTestReportsFailedAssertions drives `wb deps policy test`
// with a policy whose own assertions fail, covering the failure side of
// RunExpectations' report loop (both with and without a detection error)
// and the final non-zero-failures refusal, none of which a policy that
// only ever passes its own assertions reaches.
func TestPolicyTestReportsFailedAssertions(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(rwi01ExpectationFailurePolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicy(t, "test", policyPath)
	if err == nil {
		t.Fatalf("wb deps policy test with failing assertions: want an error, got nil\n%s", out)
	}
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("wb deps policy test with failing assertions: exit code = %d, want exitFindings", exitCodeOf(t, err))
	}
	if !strings.Contains(out, "FAIL  github.com/acme/other/pkg: want own-repo, got third-party") {
		t.Fatalf("test output missing the group-mismatch FAIL line:\n%s", out)
	}
	if !strings.Contains(out, "FAIL  github.com/acme/does-not-match/backend:") {
		t.Fatalf("test output missing the detection-error FAIL line:\n%s", out)
	}
	if !strings.Contains(out, "2 assertion(s), 0 passed, 2 failed") {
		t.Fatalf("test output missing the summary line:\n%s", out)
	}
}
