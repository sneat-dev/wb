package hooks

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

var lintableScriptExtensions = []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"}

// isLintInput reports whether a changed file can change a JS/TS lint result:
// a script, or the package, workspace, TypeScript, or ESLint configuration
// the linter reads.
func isLintInput(file string) bool {
	base := path.Base(file)
	switch {
	case base == "package.json", base == "nx.json", base == "project.json",
		strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json"),
		strings.HasPrefix(base, ".eslintrc"), strings.HasPrefix(base, "eslint.config."):
		return true
	}
	return isLintableScript(file)
}

func isLintableScript(file string) bool {
	for _, extension := range lintableScriptExtensions {
		if strings.HasSuffix(file, extension) {
			return true
		}
	}
	return false
}

func ignoredNodeDir(rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if segment == "node_modules" || segment == ".git" {
			return true
		}
	}
	return false
}

// packageManager picks the package manager from the nearest lockfile at or
// above a repository-relative directory, defaulting to npm.
func packageManager(repoRoot, dir string) string {
	for _, candidate := range []struct{ lockfile, manager string }{
		{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"},
	} {
		if _, ok := nearestMarkerDir(repoRoot, dir, candidate.lockfile); ok {
			return candidate.manager
		}
	}
	return "npm"
}

// execTool spells "run this package's binary" for each package manager.
func execTool(manager, tool string, args ...string) []string {
	switch manager {
	case "pnpm":
		return append([]string{"pnpm", "exec", tool}, args...)
	case "yarn":
		return append([]string{"yarn", tool}, args...)
	case "bun":
		return append([]string{"bun", "x", tool}, args...)
	}
	return append([]string{"npm", "exec", "--no", "--", tool}, args...)
}

func hasPackageScript(packageJSON, script string) bool {
	content, err := os.ReadFile(packageJSON)
	if err != nil {
		return false
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(content, &manifest) != nil {
		return false
	}
	return strings.TrimSpace(manifest.Scripts[script]) != ""
}

// prePushNode lints only when the push changes JS/TS sources, and only lints:
// tests and builds belong to landing and CI. An Nx workspace runs
// `nx affected -t lint` against the push base; any other package runs its own
// `lint` script, per package.json nearest each changed file.
func (r lintRunner) prePushNode() int {
	updates, skip := r.pushScope()
	if skip {
		return 0
	}
	files, all := r.pushedFiles(updates)
	var scripts []string
	for _, file := range files {
		if isLintInput(file) && !ignoredNodeDir(file) {
			scripts = append(scripts, file)
		}
	}
	if !all && len(scripts) == 0 {
		r.say("WB hook: this push changes no JS/TS source or lint configuration; skipping lint.")
		return 0
	}
	root := r.options.RepoRoot
	if _, err := os.Stat(filepath.Join(root, "nx.json")); err == nil {
		return r.nxAffectedLint(updates)
	}
	var packages []string
	if all {
		packages = allMarkerDirs(root, "package.json", ignoredNodeDir)
	} else {
		seen := map[string]bool{}
		for _, file := range scripts {
			if dir, ok := nearestMarkerDir(root, path.Dir(file), "package.json"); ok && !seen[dir] {
				seen[dir] = true
				packages = append(packages, dir)
			}
		}
		sort.Strings(packages)
	}
	failed := false
	for _, pkg := range packages {
		dir := filepath.Join(root, filepath.FromSlash(pkg))
		if !hasPackageScript(filepath.Join(dir, "package.json"), "lint") {
			continue
		}
		manager := packageManager(root, pkg)
		if ok, blocked := r.nodeToolsPresent(manager, pkg); blocked {
			return 1
		} else if !ok {
			continue
		}
		switch code, err := r.governed(dir, nil, prePushCheckTimeout, manager, "run", "lint"); {
		case err != nil:
			r.say("WB hook: lint could not run in package %s (%v); CI remains the lint gate.", pkg, err)
		case code != 0:
			r.say("WB hook: lint failed in package %s.", pkg)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// nodeToolsPresent reports whether the package manager, node, and the
// installed dependencies the linter lives in are all present. Missing
// dependencies (a fresh worktree before its first install) skip lint with a
// note rather than block: the linter is not there to run, and CI lints.
func (r lintRunner) nodeToolsPresent(manager, dir string) (ok bool, blocked bool) {
	for _, tool := range []string{manager, "node"} {
		if _, err := r.options.Tools.LookPath(tool); err != nil {
			r.say("WB hook: required Node tool not found on PATH: %s.", tool)
			return false, true
		}
	}
	if !hasInstalledDependencies(r.options.RepoRoot, dir) {
		r.say("WB hook: dependencies are not installed for %s (no node_modules); skipping lint, CI remains the lint gate. Run %s install to lint locally.", dir, manager)
		return false, false
	}
	return true, false
}

// hasInstalledDependencies reports whether a node_modules directory exists at
// or above a repository-relative directory, within the repository.
func hasInstalledDependencies(repoRoot, dir string) bool {
	candidate := path.Clean(dir)
	for {
		if info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(candidate), "node_modules")); err == nil && info.IsDir() {
			return true
		}
		if candidate == "." {
			return false
		}
		candidate = path.Dir(candidate)
	}
}

func (r lintRunner) nxAffectedLint(updates []RefUpdate) int {
	manager := packageManager(r.options.RepoRoot, ".")
	if ok, blocked := r.nodeToolsPresent(manager, "."); blocked {
		return 1
	} else if !ok {
		return 0
	}
	type span struct{ base, head string }
	var spans []span
	seen := map[span]bool{}
	for _, update := range updates {
		base, ok := r.pushBase(update)
		if !ok {
			r.say("WB hook: no local base for %s; linting every Nx project.", update.LocalRef)
			code, err := r.governed(r.options.RepoRoot, nil, prePushCheckTimeout, execTool(manager, "nx", "run-many", "-t", "lint")...)
			return r.nxResult(code, err)
		}
		candidate := span{base: base, head: update.LocalSHA}
		if !seen[candidate] {
			seen[candidate] = true
			spans = append(spans, candidate)
		}
	}
	for _, candidate := range spans {
		code, err := r.governed(r.options.RepoRoot, nil, prePushCheckTimeout,
			execTool(manager, "nx", "affected", "-t", "lint", "--base="+candidate.base, "--head="+candidate.head)...)
		if result := r.nxResult(code, err); result != 0 {
			return result
		}
	}
	return 0
}

func (r lintRunner) nxResult(code int, err error) int {
	if err != nil {
		r.say("WB hook: nx lint could not run (%v); CI remains the lint gate.", err)
		return 0
	}
	if code != 0 {
		r.say("WB hook: nx lint failed for the projects this push affects.")
		return 1
	}
	return 0
}
