package agentguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// finding is one reason a tool call was refused.
type finding struct {
	// Location is the canonical clone the call would have written into.
	Location Location
	// Detail names the specific construct that was recognised, in the words
	// an agent should read back — "git reset", "shell redirection", and so on.
	Detail string
	// GovernedCommand is the validation command an agent must submit through
	// `wb run --` so WB can measure, coalesce, and schedule it. It is set only
	// inside a managed worktree; ordinary human shells and foreign checkouts
	// remain outside this agent-hook policy.
	GovernedCommand []string
	// Message, when set, is the complete refusal text and bypasses every
	// other field's wording in refusal(). Policies that are not about a
	// canonical-clone write (missing-model dispatch, hook bypass,
	// auto-tagging, a literal report path, a claimed repository) set this
	// instead of relying on the canonical-clone phrasing built from Location.
	Message string
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
	return inspectBashDepth(command, workingDirectory, projectsRoot, 0)
}

// maxShellUnwrapDepth bounds how many `bash -c`/`sh -c`/`zsh -c` payloads this
// scanner recurses into (see shellInterpreters below). A real invocation is
// unwrapped once or twice; the bound exists only to guarantee termination
// against a pathological or adversarial chain, and hitting it fails open
// exactly like every other construct this scanner cannot model — see the
// package doc's "known blind spots".
const maxShellUnwrapDepth = 8

// inspectBashDepth is inspectBash's recursive engine. depth counts how many
// `-c` payloads have already been unwrapped to reach command, so recursion
// into a nested `bash -c "bash -c '...'"` terminates.
func inspectBashDepth(command, workingDirectory, projectsRoot string, depth int) *finding {
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
		// wb#500 review (Blocker 2): gh.go's doc comment, ai/skills/wb-hooks/
		// SKILL.md and ai/capabilities.json all claimed the gh pr merge
		// refusal reaches "chained, subshelled ... everywhere", but a
		// `bash -c "gh pr merge 123"` payload read as one opaque quoted word
		// and nothing inside it was ever inspected. Recurse into the payload
		// with the same inspector so a shape it refuses directly is refused
		// the same way wrapped in bash/sh/zsh -c, including when that payload
		// itself chains or nests another `-c` once more.
		if shellInterpreters[name] && depth < maxShellUnwrapDepth {
			if payload, ok := shellDashCPayload(words); ok {
				if result := inspectBashDepth(payload, workingDirectory, projectsRoot, depth+1); result != nil {
					return result
				}
				continue
			}
		}
		// wb#493: a read-only `--help`/`-h`/`help` invocation of an otherwise
		// guarded tool was refused the same as the write it was only asking
		// about — `specscore feature change-status --help` named a write verb
		// in its arguments and the verb scan does not know where in the
		// invocation that verb sits. requestsHelp recognises a shape that
		// differs by tool — specscore's own trailing --help/-h or `help`
		// subcommand always prints help no matter how many subcommand words
		// precede it, but go/npm/pnpm/yarn/bun get only their bare top-level
		// `--help`/`-h`/`help`, because a subcommand in front of --help/-h on
		// those five can run a script or program for real instead of printing
		// help — see requestsHelp's own doc for the real-binary findings that
		// forced the split. So skipping every write check for a recognised
		// shape never hides a real write. Any other flag mixed onto the line,
		// including one positioned to swallow --help/-h as its own value, is
		// inspected normally instead of bypassed; see requestsHelp's own doc.
		// The set is deliberately narrow — see helpBypassTools's own doc — so
		// this can never again turn into the wb#500 review's Blocker 1 (`rm -rf
		// <canonical-clone-file> -h`, `gh pr merge 123 --subject --help`), its
		// second-round Should-fix 1 (`specscore ... --caller --help --to
		// Approved`, `npm run build -- --help`), or its third-round finding
		// against the real binaries (`pnpm run build --help`, `bun run build
		// --help`, and `go run . --help` all actually ran instead of printing
		// help).
		if helpBypassTools[name] && requestsHelp(name, words) {
			continue
		}
		if managedWorktree(workingDirectory) && isGovernedValidation(name, words) {
			return &finding{Detail: strings.Join(words, " "), GovernedCommand: words}
		}
		if result := inspectCommand(name, words, current.Words, workingDirectory, projectsRoot); result != nil {
			return result
		}
	}
	return nil
}

// shellInterpreters names the shells whose `-c <payload>` this guard recurses
// into with the same inspector. See inspectBashDepth's wb#500 comment.
var shellInterpreters = map[string]bool{"bash": true, "sh": true, "zsh": true}

// shellDashCPayload reports the command-string argument to a shell's `-c`
// flag, tolerant of it being bundled with other short flags (`-lc`, `-ic`,
// the common "login shell running one command" spelling agent harnesses use).
// bash/sh/zsh all treat -c the same way: once it is seen, the very next word
// is the command string, never another shell flag, so the first word after it
// is always the payload.
func shellDashCPayload(words []string) (string, bool) {
	for index := 1; index < len(words); index++ {
		word := words[index]
		if !strings.HasPrefix(word, "-") || strings.HasPrefix(word, "--") {
			continue
		}
		if !strings.ContainsRune(word[1:], 'c') {
			continue
		}
		if index+1 < len(words) {
			return words[index+1], true
		}
		return "", false
	}
	return "", false
}

// managedWorktree reports whether directory is enclosed by a WB worktree
// manifest. This is intentionally a metadata-only walk: the PreToolUse hook
// runs before every agent command, so spawning Git or parsing the full manifest
// here would tax the entire fleet. An absent or unreadable marker fails open.
func managedWorktree(directory string) bool {
	if directory == "" {
		return false
	}
	current, ok := absolutePath(directory)
	if !ok {
		return false
	}
	for range maxAncestorWalk {
		info, err := os.Stat(filepath.Join(current, ".wb", "local", "manifest.yaml"))
		if err == nil && !info.IsDir() {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
	return false
}

var validationScript = regexp.MustCompile(`^(test|build|lint|e2e|coverage)(:|$)`)

// isGovernedValidation names CPU-heavy validation that agents must submit
// through `wb run --`. Fast formatters stay direct and close to edits.
func isGovernedValidation(name string, words []string) bool {
	name = filepath.Base(name)
	switch name {
	case "go":
		return containsAnyWord(words[1:], "test", "vet", "build")
	case "golangci-lint", "staticcheck", "pytest", "vitest", "jest", "mocha":
		return true
	case "nx":
		return containsAnyWord(words[1:], "test", "build", "lint", "e2e", "run-many", "affected")
	case "npm", "pnpm", "yarn", "bun":
		return packageManagerValidation(words[1:])
	case "npx":
		nested := firstNonFlag(words[1:])
		if len(nested) == 0 {
			return false
		}
		return isGovernedValidation(filepath.Base(nested[0]), nested)
	case "cargo":
		return containsAnyWord(words[1:], "test", "build", "check", "clippy")
	}
	return false
}

func packageManagerValidation(arguments []string) bool {
	words := firstNonFlag(arguments)
	if len(words) == 0 {
		return false
	}
	if validationScript.MatchString(words[0]) {
		return true
	}
	switch words[0] {
	case "run":
		return len(words) > 1 && validationScript.MatchString(words[1])
	case "exec", "dlx":
		return len(words) > 1 && isGovernedValidation(filepath.Base(words[1]), words[1:])
	}
	return false
}

// helpBypassTools names the tools wb#493 exists for. Each treats
// --help/-h/help as terminal when requestsHelp recognises the invocation's
// shape as a genuine help request for that specific tool (see requestsHelp)
// — it prints help and does nothing else — so skipping their write-verb
// check for that shape never hides a real write.
//
// The set is deliberately narrow and must stay that way:
//
//   - It must never include a file mutator (rm, mv, cp, chmod, chown, rsync,
//     ...): -h means something else entirely on several of them (rsync's
//     human-readable sizes; BSD chmod/chown/cp's "operate on the symlink
//     itself"), so treating it as a help request there silently turns off
//     the canonical-clone guard for an ordinary, unrelated flag.
//   - It must never include gh: `gh pr merge`'s own -h/--help recognition
//     lives in gh.go's ghRequestsHelp, because a value-taking flag positioned
//     just before it (`gh pr merge 123 --subject --help`) makes --help the
//     VALUE of --subject, not a help request — pflag hands a value-taking
//     flag the very next token unconditionally, so that call really does
//     merge with the literal subject text "--help". A blind "--help anywhere"
//     scan, applied ahead of gh's own guard the way it once was, let that
//     merge through unrefused (wb#500 review, Blocker 1).
var helpBypassTools = map[string]bool{
	"specscore": true, "go": true,
	"npm": true, "pnpm": true, "yarn": true, "bun": true,
}

// requestsHelp reports whether a command's own words are shaped as a genuine
// help request, never merely whether --help/-h/help appears somewhere on the
// line. Which shape it trusts depends on name, because a subcommand
// positioned in front of --help/-h is safe on some of these tools and not on
// others:
//
//   - specscore is cobra-based: its own trailing --help/-h always prints help
//     no matter how many subcommand words precede it, and its own `help`
//     subcommand always prints help no matter what subcommand words follow
//     it. cobraStyleHelp recognises both shapes.
//   - go, npm, pnpm, and yarn and bun are NOT safe with a subcommand in front
//     of --help/-h: each has at least one subcommand that passes positional
//     arguments straight through to a script or program instead of stopping
//     at its own flag parser, so "<tool> <subcommand...> --help" can run
//     that script or program for real instead of printing help. Confirmed
//     against the real binaries (wb#500 third review): `pnpm run build
//     --help` and `bun run build --help` ran the build script; `go run .
//     --help` ran the program. pnpm, yarn, and bun also run a package.json
//     script when invoked WITHOUT "run" at all (`pnpm build --help` runs the
//     "build" script exactly like `pnpm run build --help` does), so no
//     denylist of pass-through verbs is safe for them either — the only
//     shape trusted for these five tools is the bare top-level invocation:
//     bareStyleHelp recognises only "<tool> --help"/"<tool> -h" with nothing
//     else after the program name, and "<tool> help" followed by zero or
//     more non-flag words.
//
// Either shape falls through to normal inspection — never bypassed — on any
// other flag mixed onto the line, --help/-h not the very last word (bare
// shape) or not the only trailing word (cobra shape), more than one
// --help/-h, or a `--` argument separator anywhere. This is deliberately
// conservative rather than value-flag aware per tool (contrast gh.go's
// ghPrMergeValueFlags, built for the one command this guard already knows
// every value-taking flag of): a flag positioned just before --help/-h can
// swallow it as that flag's own value instead of a genuine help request —
// pflag hands a value-taking flag the next token unconditionally, so
// `specscore feature change-status <id> --caller --help --to Approved`
// really calls change-status with --caller="--help", it does not print help
// (wb#500 second review, Should-fix 1) — and a `--` separator hands
// everything after it to the program being invoked, not to the wrapper, so
// `npm run build -- --help` really runs the build script with --help as ITS
// argument; npm never sees a help request at all (same review).
//
// See wb#493 and wb#500's second and third reviews. Callers must gate this
// on helpBypassTools first — see its doc for why.
func requestsHelp(name string, words []string) bool {
	arguments := words[1:]
	if name == "specscore" {
		return cobraStyleHelp(arguments)
	}
	return bareStyleHelp(arguments)
}

// cobraStyleHelp recognises the two help shapes safe for a cobra-based CLI —
// currently only specscore, see requestsHelp's own doc for why the other
// helpBypassTools tools get bareStyleHelp instead:
//
//   - Zero or more subcommand words, none of them starting with "-", followed
//     by exactly one final --help or -h with nothing after it. `specscore
//     feature change-status --help`.
//   - The literal word "help" first, with no word anywhere after it starting
//     with "-". `specscore help feature change-status`.
func cobraStyleHelp(arguments []string) bool {
	if len(arguments) == 0 {
		return false
	}
	if arguments[0] == "help" {
		return noWordStartsWithDash(arguments[1:])
	}
	last := len(arguments) - 1
	if arguments[last] != "--help" && arguments[last] != "-h" {
		return false
	}
	return noWordStartsWithDash(arguments[:last])
}

// bareStyleHelp recognises the two help shapes safe for go, npm, pnpm, yarn,
// and bun — see requestsHelp's own doc for the real-binary findings that
// forced this to be narrower than cobraStyleHelp:
//
//   - The bare program invocation with --help or -h and nothing else after
//     it: `go --help`, `pnpm -h`. A subcommand word in front of --help/-h is
//     never recognised here, unlike cobraStyleHelp.
//   - The literal word "help" first, with no word anywhere after it starting
//     with "-". `go help build`, `npm help install`.
func bareStyleHelp(arguments []string) bool {
	if len(arguments) == 0 {
		return false
	}
	if arguments[0] == "help" {
		return noWordStartsWithDash(arguments[1:])
	}
	return len(arguments) == 1 && (arguments[0] == "--help" || arguments[0] == "-h")
}

// noWordStartsWithDash reports whether none of words looks like a flag. Both
// help shapes use it to keep a stray flag anywhere in the trusted region from
// being waved through alongside a genuine --help/-h/help.
func noWordStartsWithDash(words []string) bool {
	for _, word := range words {
		if strings.HasPrefix(word, "-") {
			return false
		}
	}
	return true
}

func firstNonFlag(words []string) []string {
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		words = words[1:]
	}
	return words
}

func containsAnyWord(words []string, targets ...string) bool {
	for _, word := range words {
		for _, target := range targets {
			if word == target {
				return true
			}
		}
	}
	return false
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
// about it, and allows anything unrecognised. rawWords are the segment's
// words before commandWords stripped any leading environment assignment or
// transparent prefix (sudo, env, ...) — inspectGh needs them to read an
// inline WB_AGENTGUARD_ALLOW_GH_PR_MERGE="<reason>" that prefixes this exact
// call, the only place that override is honoured (see gh.go).
func inspectCommand(name string, words []string, rawWords []string, workingDirectory, projectsRoot string) *finding {
	switch name {
	case "git":
		return inspectGit(words[1:], workingDirectory, projectsRoot)
	case "gh":
		override, _ := leadingAssignmentValue(rawWords, ghPrMergeOverrideEnv)
		return inspectGh(words, projectsRoot, override)
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

// transparentCommandPrefixes names the wrapper commands commandWords and
// leadingAssignmentValue both strip to reach the real program name — a
// process that runs its argument as-is, changing nothing about how the guard
// should read it. Kept as the one shared table so the two prefix-stripping
// walks (the general one every recogniser sees the program name through, and
// inspectGh's escape-hatch-only one that also needs the assignment itself)
// can never drift apart: a wrapper added to one without the other would make
// an override prefixed with it silently stop being recognised even though the
// dispatch it prefixes is still stripped down to the real program and
// refused (wb#500 second review, Nit 2).
var transparentCommandPrefixes = map[string]bool{
	"sudo": true, "nohup": true, "command": true, "nice": true,
	"time": true, "stdbuf": true, "exec": true,
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
		if transparentCommandPrefixes[filepath.Base(word)] {
			words = words[1:]
			continue
		}
		if filepath.Base(word) == "env" {
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

// splitAssignment splits a word already known to be a `VAR=value` assignment
// (see isEnvironmentAssignment) into its name and value.
func splitAssignment(word string) (name, value string) {
	index := strings.IndexByte(word, '=')
	return word[:index], word[index+1:]
}

// leadingAssignmentValue reports the value a leading `VAR=value` prefix on
// words assigns to variable — directly (`VAR=value gh ...`), or via `env`
// (`env VAR=value gh ...`) — using the same prefix-stripping rules as
// commandWords. Only a leading assignment counts: that is what scopes an
// inline override to the one command it prefixes, the same way a shell does.
// The last matching assignment before the real program name wins, mirroring
// a shell's own "last one wins" semantics for a repeated variable.
//
// This exists for inspectGh's escape hatch (wb#500 review, Should-fix 3):
// commandWords strips a leading assignment and discards it, because every
// other caller only wants the real program name. inspectGh is the one caller
// that needs the assignment itself, read from the words of the exact call it
// prefixes — never from the hook process's own ambient environment, which
// would silently cover every gh pr merge for the rest of a session instead of
// the one call an operator meant to allow.
func leadingAssignmentValue(words []string, variable string) (string, bool) {
	value, found := "", false
	for len(words) > 0 {
		word := words[0]
		if isEnvironmentAssignment(word) {
			if name, assigned := splitAssignment(word); name == variable {
				value, found = assigned, true
			}
			words = words[1:]
			continue
		}
		if transparentCommandPrefixes[filepath.Base(word)] {
			words = words[1:]
			continue
		}
		if filepath.Base(word) == "env" {
			words = words[1:]
			for len(words) > 0 && (isEnvironmentAssignment(words[0]) || strings.HasPrefix(words[0], "-")) {
				if isEnvironmentAssignment(words[0]) {
					if name, assigned := splitAssignment(words[0]); name == variable {
						value, found = assigned, true
					}
				}
				words = words[1:]
			}
			continue
		}
		break
	}
	return value, found
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
