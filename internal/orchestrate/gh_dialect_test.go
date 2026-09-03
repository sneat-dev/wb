package orchestrate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The land and merge verbs must work against the `gh` actually installed on
// this fleet — 2.45 — which has neither `gh api --slurp` nor
// `gh pr checks --json`. Both were on the merge verb's critical path, so its
// checks stage failed on every run, operators fell back to raw `gh pr merge`,
// and the opt-in cleanup that should have retired the worktree never ran.
//
// This is a source-level guard rather than a behavioural one on purpose: the
// regression is a *call* reappearing, and a behavioural test only catches it on
// the paths some test happens to exercise.
//
// It reads the syntax tree rather than the text. A line-based scanner misses an
// argument list split across lines and stops at the first hit in a file, and
// both of those are exactly how the call would come back — reformatted by a
// tool, or added below one that was already there.
func TestNoSourceFileShellsOutToAGHDialectTheInstalledClientLacks(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	for _, directory := range []string{"internal", "cmd"} {
		walkGoSourceFiles(t, filepath.Join(repoRoot, directory), func(path string, file *ast.File, fset *token.FileSet) {
			for _, call := range gitHubCommandCalls(file) {
				for _, finding := range forbiddenDialects(call.arguments) {
					t.Errorf("%s:%d passes %s to gh: %s",
						path, fset.Position(call.position).Line, finding.fragment, finding.why)
				}
			}
		})
	}
}

type ghCall struct {
	position  token.Pos
	arguments []string
}

// gitHubCommandCalls collects every call whose string-literal arguments could
// be a gh argument list. It deliberately does not try to identify the callee:
// a helper renamed or wrapped would slip past that, and a false positive here
// costs a comment while a false negative costs the regression this guards.
func gitHubCommandCalls(file *ast.File) []ghCall {
	var calls []ghCall
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		arguments := make([]string, 0, len(call.Args))
		for _, argument := range call.Args {
			literal, ok := argument.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}
			arguments = append(arguments, value)
		}
		if len(arguments) > 0 {
			calls = append(calls, ghCall{position: call.Lparen, arguments: arguments})
		}
		return true
	})
	// A composite literal of strings — []string{"api", "--slurp", …} handed to
	// a runner — is the other spelling, and it is not a CallExpr.
	ast.Inspect(file, func(node ast.Node) bool {
		composite, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		arguments := make([]string, 0, len(composite.Elts))
		for _, element := range composite.Elts {
			literal, ok := element.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}
			arguments = append(arguments, value)
		}
		if len(arguments) > 0 {
			calls = append(calls, ghCall{position: composite.Lbrace, arguments: arguments})
		}
		return true
	})
	return calls
}

type dialectFinding struct {
	fragment string
	why      string
}

// forbiddenDialects reports every unsupported gh spelling in one argument list.
// It returns all of them rather than the first, because a file that grew two
// must not be reported as having grown one.
func forbiddenDialects(arguments []string) []dialectFinding {
	findings := make([]dialectFinding, 0, 2)
	for index, argument := range arguments {
		if argument == "--slurp" {
			findings = append(findings, dialectFinding{
				fragment: "--slurp",
				why:      "gh api --slurp needs a newer client; follow the link header instead (githubobserver.GetPages)",
			})
		}
		if argument == "checks" && index > 0 && arguments[index-1] == "pr" {
			findings = append(findings, dialectFinding{
				fragment: "pr checks",
				why:      "gh pr checks --json needs a newer client; read the head commit's check runs and statuses instead",
			})
		}
	}
	return findings
}

func walkGoSourceFiles(t *testing.T, root string, visit func(path string, file *ast.File, fset *token.FileSet)) {
	t.Helper()
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		parsed, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		visit(path, parsed, fset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Both evasions the previous line-based scanner allowed, as regressions: an
// argument list split across lines, and a second occurrence in the same file.
func TestDialectGuardCatchesSplitAndRepeatedArgumentLists(t *testing.T) {
	source := `package sample

func run(args ...string) {}

func first() {
	run(
		"api",
		"--paginate",
		"--slurp",
		"repos/acme/app/rules/branches/main",
	)
}

func second() {
	run("pr", "checks", "41", "--repo", "acme/app", "--json", "name")
	arguments := []string{"api", "--slurp", "/search/issues"}
	run(arguments...)
}
`
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "sample.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, call := range gitHubCommandCalls(parsed) {
		for _, finding := range forbiddenDialects(call.arguments) {
			found[finding.fragment]++
		}
	}
	if found["--slurp"] != 2 {
		t.Errorf("--slurp findings = %d, want both the split argument list and the composite literal", found["--slurp"])
	}
	if found["pr checks"] != 1 {
		t.Errorf("pr checks findings = %d, want the second occurrence in the same file", found["pr checks"])
	}
}
