package agentguard

import (
	"path/filepath"
	"strings"
)

// finding is one reason a tool call was refused.
type finding struct {
	// Location is the canonical clone the call would have written into.
	Location Location
	// Detail names the specific construct that was recognised, in the words
	// an agent should read back — "git reset", "shell redirection", and so on.
	Detail string
}

// inspectBash reports whether a Bash command would write inside a canonical
// clone, or nil when it would not, when it cannot be told, or when the write
// lands anywhere else.
//
// # Detection strategy, and what it deliberately misses
//
// There is no general shell parser here and there must not be one. The scanner
// recognises a fixed set of high-frequency, unambiguous write constructs, and
// treats everything else as an allow:
//
//   - Git subcommands that mutate the working tree, index, or history.
//   - Output redirections (>, >>) whose target lands in a canonical clone.
//   - In-place editors: sed -i, perl -i, ruby -i.
//   - File mutators: rm, mv, cp, touch, mkdir, tee, patch, chmod, and friends,
//     when an argument resolves inside a canonical clone.
//   - Generators known to write into their working directory: specscore write
//     verbs, go mod tidy / go generate, gofmt -w, package-manager installs,
//     formatter --write / --fix runs.
//
// Known blind spots, all of which fail open:
//
//   - An interpreter given an inline script (python3 - <<EOF, node -e, a
//     shell function) that writes files. The heredoc body is skipped on
//     purpose so it is never misread as shell, which also means its contents
//     are never inspected.
//   - A working directory established through a variable (cd "$REPO"), a
//     command substitution, or a shell function. The scanner does not expand,
//     so it marks the working directory unknown and allows what follows.
//   - Any binary not on the recognised list, including one that writes.
//   - A relative path whose base directory could not be resolved.
//
// The bias is deliberate. A guard that blocks legitimate work is a guard
// agents learn to route around, and a routed-around guard protects nothing.
func inspectBash(command, sessionCwd, projectsRoot string) *finding {
	workingDirectory := ""
	if absolute, ok := absolutePath(sessionCwd); ok {
		workingDirectory = absolute
	}
	for _, current := range splitSegments(command) {
		if result := inspectRedirects(current, workingDirectory, projectsRoot); result != nil {
			return result
		}
		words := commandWords(current.Words)
		if len(words) == 0 {
			continue
		}
		name := filepath.Base(words[0])
		if name == "cd" || name == "pushd" {
			workingDirectory = applyChangeDirectory(workingDirectory, words[1:])
			continue
		}
		if result := inspectCommand(name, words, workingDirectory, projectsRoot); result != nil {
			return result
		}
	}
	return nil
}

// inspectRedirects refuses `... > <inside a canonical clone>`. This is the one
// construct that writes a file without naming a program that writes files, and
// `cat > file <<EOF` is how agents most often create one.
func inspectRedirects(current segment, workingDirectory, projectsRoot string) *finding {
	for _, target := range current.RedirectTargets {
		if target == "" || strings.HasPrefix(target, "/dev/") || isAllDigits(target) {
			continue
		}
		if location, ok := canonicalTarget(workingDirectory, target, projectsRoot); ok {
			return &finding{Location: location, Detail: "a shell redirection into " + target}
		}
	}
	return nil
}

// inspectCommand dispatches one simple command to whichever recogniser knows
// about it, and allows anything unrecognised.
func inspectCommand(name string, words []string, workingDirectory, projectsRoot string) *finding {
	switch name {
	case "git":
		return inspectGit(words[1:], workingDirectory, projectsRoot)
	case "wb":
		// WB is the remedy the refusal names, and the only tool authorised to
		// write into a canonical clone. Refusing it would make the guard's own
		// advice unfollowable.
		return nil
	case "sed", "gsed", "perl", "ruby":
		return inspectInPlaceEditor(name, words, workingDirectory, projectsRoot)
	case "specscore":
		return inspectGenerator(name, words, workingDirectory, projectsRoot, specscoreWriteVerbs, nil)
	case "go":
		return inspectGenerator(name, words, workingDirectory, projectsRoot, goWriteVerbs, nil)
	case "npm", "pnpm", "yarn", "bun":
		return inspectGenerator(name, words, workingDirectory, projectsRoot, packageManagerWriteVerbs, nil)
	case "gofmt", "goimports", "prettier", "eslint", "black", "ruff", "biome":
		return inspectFormatter(name, words, workingDirectory, projectsRoot)
	}
	if fileMutators[name] {
		return inspectFileMutator(name, words, workingDirectory, projectsRoot)
	}
	return nil
}

// commandWords drops leading environment assignments and transparent command
// prefixes so the recognisers see the real program name.
func commandWords(words []string) []string {
	for len(words) > 0 {
		word := words[0]
		if isEnvironmentAssignment(word) {
			words = words[1:]
			continue
		}
		switch filepath.Base(word) {
		case "sudo", "nohup", "command", "nice", "time", "stdbuf", "exec":
			words = words[1:]
			continue
		case "env":
			words = words[1:]
			for len(words) > 0 && (isEnvironmentAssignment(words[0]) || strings.HasPrefix(words[0], "-")) {
				words = words[1:]
			}
			continue
		}
		break
	}
	return words
}

func isEnvironmentAssignment(word string) bool {
	index := strings.IndexByte(word, '=')
	if index <= 0 {
		return false
	}
	for position, character := range word[:index] {
		if !isVariableNameCharacter(character, position) {
			return false
		}
	}
	return true
}

// isVariableNameCharacter reports whether a rune may appear at position in a
// shell variable name. A digit is legal anywhere but the first position.
func isVariableNameCharacter(character rune, position int) bool {
	switch {
	case character == '_':
		return true
	case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z':
		return true
	case character >= '0' && character <= '9':
		return position > 0
	default:
		return false
	}
}

// applyChangeDirectory follows a `cd`. A target the scanner cannot resolve
// without expanding the shell — anything holding $, `, or * — makes the
// working directory unknown, which allows everything after it. Guessing there
// would be worse than allowing: a wrong guess refuses correct work.
func applyChangeDirectory(workingDirectory string, arguments []string) string {
	target := ""
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		target = argument
		break
	}
	if target == "" || strings.ContainsAny(target, "$`*?") {
		return ""
	}
	resolved, ok := resolveAgainst(workingDirectory, target)
	if !ok {
		return ""
	}
	return resolved
}

// canonicalTarget resolves one path argument and reports the canonical clone
// it lands in, if any.
func canonicalTarget(workingDirectory, path, projectsRoot string) (Location, bool) {
	if strings.ContainsAny(path, "$`") {
		return Location{}, false
	}
	resolved, ok := resolveAgainst(workingDirectory, path)
	if !ok {
		return Location{}, false
	}
	location := Classify(projectsRoot, resolved)
	if location.Kind != KindCanonical {
		return Location{}, false
	}
	return location, true
}

// canonicalWorkingDirectory reports the canonical clone a command would run
// in, if any.
func canonicalWorkingDirectory(workingDirectory, projectsRoot string) (Location, bool) {
	if workingDirectory == "" {
		return Location{}, false
	}
	location := Classify(projectsRoot, workingDirectory)
	if location.Kind != KindCanonical {
		return Location{}, false
	}
	return location, true
}

var fileMutators = map[string]bool{
	"rm": true, "mv": true, "cp": true, "touch": true, "mkdir": true,
	"rmdir": true, "tee": true, "patch": true, "chmod": true, "chown": true,
	"truncate": true, "install": true, "ln": true, "dd": true, "rsync": true,
	"unzip": true, "shred": true,
}

// inspectFileMutator refuses a file-touching utility whose arguments name a
// path inside a canonical clone. Flags are skipped; a bare `rm` in a canonical
// working directory with no path argument is not refused, because without a
// path there is nothing to judge.
func inspectFileMutator(name string, words []string, workingDirectory, projectsRoot string) *finding {
	for _, argument := range words[1:] {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		if location, ok := canonicalTarget(workingDirectory, argument, projectsRoot); ok {
			return &finding{Location: location, Detail: name + " " + argument}
		}
	}
	return nil
}

// inspectInPlaceEditor refuses sed/perl/ruby only when they were asked to edit
// in place. Without -i they read, and reading a canonical clone is fine.
func inspectInPlaceEditor(name string, words []string, workingDirectory, projectsRoot string) *finding {
	inPlace := false
	for _, argument := range words[1:] {
		if !strings.HasPrefix(argument, "-") || strings.HasPrefix(argument, "--") {
			continue
		}
		if strings.Contains(argument, "i") {
			inPlace = true
			break
		}
	}
	if !inPlace {
		return nil
	}
	for _, argument := range words[1:] {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		if location, ok := canonicalTarget(workingDirectory, argument, projectsRoot); ok {
			return &finding{Location: location, Detail: name + " -i " + argument}
		}
	}
	return nil
}

var specscoreWriteVerbs = map[string]bool{
	"new": true, "init": true, "create": true, "change-status": true,
	"archive": true, "recur": true, "relocate": true, "promote": true,
	"scaffold": true, "capture": true, "close": true, "repair": true,
}

var goWriteVerbs = map[string]bool{
	"generate": true, "get": true, "work": true,
}

var packageManagerWriteVerbs = map[string]bool{
	"install": true, "i": true, "add": true, "remove": true, "rm": true,
	"uninstall": true, "update": true, "up": true, "link": true, "dedupe": true,
	"prune": true,
}

// inspectGenerator refuses a tool that writes into its own working directory
// when that directory is a canonical clone and the invocation names a verb
// known to write. A read verb — `specscore lesson list`, `go build` — is
// allowed, because reading a canonical clone is exactly what it is for.
func inspectGenerator(
	name string,
	words []string,
	workingDirectory, projectsRoot string,
	writeVerbs map[string]bool,
	extra func(words []string) bool,
) *finding {
	location, ok := canonicalWorkingDirectory(workingDirectory, projectsRoot)
	if !ok {
		return nil
	}
	verb := ""
	for _, argument := range words[1:] {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		if writeVerbs[argument] {
			verb = argument
			break
		}
	}
	if verb == "" {
		// `go mod tidy` reaches here as mod + tidy, neither of which is in the
		// verb set on its own.
		if name == "go" && containsWord(words, "mod") && containsWord(words, "tidy") {
			verb = "mod tidy"
		} else if extra != nil && extra(words) {
			verb = words[1]
		} else {
			return nil
		}
	}
	return &finding{Location: location, Detail: name + " " + verb + " with the clone as the working directory"}
}

// inspectFormatter refuses a formatter or linter asked to rewrite files while
// the canonical clone is its working directory or its target.
func inspectFormatter(name string, words []string, workingDirectory, projectsRoot string) *finding {
	rewrites := false
	for _, argument := range words[1:] {
		switch argument {
		case "-w", "--write", "--fix", "-i", "format":
			rewrites = true
		}
		if strings.HasPrefix(argument, "--fix") || strings.HasPrefix(argument, "--write") {
			rewrites = true
		}
	}
	if !rewrites {
		return nil
	}
	for _, argument := range words[1:] {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		if location, ok := canonicalTarget(workingDirectory, argument, projectsRoot); ok {
			return &finding{Location: location, Detail: name + " rewriting " + argument}
		}
	}
	if location, ok := canonicalWorkingDirectory(workingDirectory, projectsRoot); ok {
		return &finding{Location: location, Detail: name + " rewriting files in the clone"}
	}
	return nil
}

func containsWord(words []string, target string) bool {
	for _, word := range words {
		if word == target {
			return true
		}
	}
	return false
}
