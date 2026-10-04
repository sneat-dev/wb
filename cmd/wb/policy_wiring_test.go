package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rootPolicyDocument = `
groups:
  - {name: own, match: ["<self>/..."]}
  - {name: external, match: ["..."]}
types:
  - name: service
    detect: ["example.com/*"]
    scopes: {source: {allow: [own]}}
`

func TestDepsPolicyRootRegistersNineChildrenAndUsesCurrentNativePolicy(t *testing.T) {
	t.Parallel()
	first, second := t.TempDir(), t.TempDir()
	module := filepath.Join(second, "acme", "app")
	cwCovWriteFile(t, filepath.Join(module, "go.mod"), "module example.com/app\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(module, "app.go"), "package app\n")
	cwCovWriteFile(t, filepath.Join(second, "acme", "policy", "backend.yaml"), rootPolicyDocument)
	inv := testInvocation(t, first)
	deps := newDepsCmd(inv)
	family, _, e := deps.Find([]string{"policy"})
	if e != nil {
		t.Fatal(e)
	}
	deps.RemoveCommand(family)
	for _, name := range []string{"check", "explain", "show", "validate", "test", "init", "report", "drift", "impact"} {
		child, _, err := family.Find([]string{name})
		if err != nil || child.Name() != name {
			t.Fatalf("registry %s: %v", name, err)
		}
	}
	inv.projectsRoot = second
	family.SilenceUsage = true
	family.SilenceErrors = true
	var out bytes.Buffer
	family.SetOut(&out)
	family.SetErr(&out)
	family.SetArgs([]string{"check", module, "--policy", "acme/policy//backend.yaml", "--format", "json"})
	if e = family.Execute(); e != nil {
		t.Fatal(e)
	}
	var decoded map[string]any
	if e = json.Unmarshal(out.Bytes(), &decoded); e != nil || !strings.Contains(out.String(), "example.com/app") {
		t.Fatalf("native result %s %v", out.String(), e)
	}
	out.Reset()
	family.SetArgs([]string{"init", module, "--policy", "acme/policy//backend.yaml"})
	if e = family.Execute(); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(module, ".wb-deps-policy.yaml"))
	if e != nil || string(raw) != "policy: acme/policy//backend.yaml\n" || !strings.Contains(out.String(), "running check...") {
		t.Fatalf("durable init %q %q %v", raw, out.String(), e)
	}
	out.Reset()
	family.SetArgs([]string{"check", t.TempDir()})
	e = family.Execute()
	var coded *exitError
	if !errors.As(e, &coded) || coded.code != exitUsage || !strings.Contains(e.Error(), "no go.mod found") {
		t.Fatalf("actual usage identity %T %v", e, e)
	}
}
func TestDepsPolicyRootFleetUsesActualDiscoveryAndTypedFindings(t *testing.T) { //nolint:paralleltest // Real GitHub PATH fixture changes process environment.
	root := t.TempDir()
	app := filepath.Join(root, "acme", "app")
	initTestRepository(t, app)
	cwCovWriteFile(t, filepath.Join(app, "go.mod"), "module example.com/app\n\ngo 1.26\n")
	cwCovWriteFile(t, filepath.Join(app, "app.go"), "package app\nimport _ \"example.com/other\"\n")
	cwCovWriteFile(t, filepath.Join(root, "acme", "policy", "backend.yaml"), rootPolicyDocument)
	cwCovWriteFile(t, filepath.Join(app, ".wb-deps-policy.yaml"), "policy: acme/policy//backend.yaml\n")
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)
	inv := testInvocation(t, t.TempDir())
	deps := newDepsCmd(inv)
	family, _, e := deps.Find([]string{"policy"})
	if e != nil {
		t.Fatal(e)
	}
	deps.RemoveCommand(family)
	inv.projectsRoot = root
	var out bytes.Buffer
	family.SetOut(&out)
	family.SetErr(&out)
	family.SilenceUsage = true
	family.SilenceErrors = true
	family.SetArgs([]string{"report", "--format", "json"})
	e = family.Execute()
	var coded *exitError
	if !errors.As(e, &coded) || coded.code != exitFindings || !strings.Contains(out.String(), "acme/app") || !strings.Contains(out.String(), `"governed": true`) {
		t.Fatalf("native selection/result %q %T %v", out.String(), e, e)
	}
}
