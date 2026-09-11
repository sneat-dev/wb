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
// Every recogniser sees the program through two layers. stripCommandPrefixes
// reads past leading assignments, the shell keywords, and the wrapper programs
// named in transparentCommandPrefixes together with their own options.
// shellDashCPayloads reads the -c payload of bash, sh, zsh, dash and ksh.
//
// Known blind spots, all of which fail open:
//
//   - An interpreter given an inline script (python3 -c, python3 - <<EOF,
//     node -e, a shell function) that writes files. The heredoc body is
//     skipped on purpose so it is never misread as shell, which also means
//     its contents are never inspected.
//   - A command the shell builds or reads at run time. That covers eval, a
//     here-string fed to a shell (bash <<< '...'), a script file (./land.sh,
//     bash script.sh), backticks, a command substitution's output used as a
//     command word, ANSI-C quoting ($'...') and env -S's split string.
//   - A wrapper program that is not in transparentCommandPrefixes (ssh, watch,
//     arch, script, ...), and an interpreter that is not in shellInterpreters
//     (fish, csh).
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
	return inspectBashDepth(command, workingDirectory, projectsRoot, 0, false)
}

// maxShellUnwrapDepth bounds how many shell -c payloads this scanner recurses
// into (see shellInterpreters below). A real invocation is unwrapped once or
// twice. The bound exists only to guarantee termination against a
// pathological or adversarial chain, and hitting it fails open exactly like
// every other construct this scanner cannot model. See inspectBash's "known
// blind spots".
const maxShellUnwrapDepth = 8

// inspectBashDepth is inspectBash's recursive engine. depth counts how many
// -c payloads have already been unwrapped to reach command, so recursion into
// a nested `bash -c "bash -c '...'"` terminates. governed is true when command
// is a payload that `wb run --` runs, so the governed-validation gate never
// sends back to wb run what wb run is already running.
func inspectBashDepth(command, workingDirectory, projectsRoot string, depth int, governed bool) *finding {
	for _, current := range splitSegments(command) {
		if result := inspectRedirects(current, workingDirectory, projectsRoot); result != nil {
			return result
		}
		stripped := stripCommandPrefixes(current.Words)
		words := stripped.Words
		if len(words) == 0 {
			continue
		}
		segmentGoverned := governed || stripped.Governed
		name := filepath.Base(words[0])
		if name == "cd" || name == "pushd" {
			workingDirectory = applyChangeDirectory(workingDirectory, words[1:])
			continue
		}
		// wb#500 review (Blocker 2): a `bash -c "gh pr merge 123"` payload
		// read as one opaque quoted word, so nothing inside it was ever
		// inspected. Recurse into the payload with the same inspector, so a
		// shape refused directly is refused the same way when wrapped,
		// including when the payload chains or nests another -c. The final
		// review (S2) showed the payload is not simply the word after -c; see
		// shellDashCPayloads.
		if readings, ok := shellInterpreters[name]; ok && depth < maxShellUnwrapDepth {
			if payloads := shellDashCPayloads(words, readings); len(payloads) > 0 {
				for _, payload := range payloads {
					if result := inspectBashDepth(payload, workingDirectory, projectsRoot, depth+1, segmentGoverned); result != nil {
						return result
					}
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
		// `wb run -- <command>` is the gateway this gate sends heavy
		// validation to. The command it runs is still judged by every other
		// policy below (a gh pr merge, a canonical-clone write), but it is
		// never sent back to wb run.
		if !segmentGoverned && managedWorktree(workingDirectory) && isGovernedValidation(name, words) {
			return &finding{Detail: strings.Join(words, " "), GovernedCommand: words}
		}
		if result := inspectCommand(name, words, current.Words, workingDirectory, projectsRoot); result != nil {
			return result
		}
	}
	return nil
}

// shellOptionReading is one way a shell reads the option words in front of
// its -c payload. The shells agree on everything shellDashCPayloads relies on
// and differ only in which options take a value.
type shellOptionReading struct {
	// attachedO makes -o take the rest of its own word as its value when any
	// remains (`-oerrexit`), and the next word only when nothing remains.
	// zsh and ksh read it that way. bash and dash give every o in a word the
	// next word, so `bash -oerrexit -c ...` spends "-c" as the option name
	// and fails.
	attachedO bool
	// valueO makes -O/+O take the next word: bash's shopt names, as in
	// `-O extglob`. On zsh, -O is the one-letter CORRECT_ALL flag and takes
	// nothing.
	valueO bool
	// longValues are the long options that take the next word: bash's
	// --rcfile and --init-file.
	longValues map[string]bool
}

var (
	bashOptionReading = shellOptionReading{valueO: true, longValues: map[string]bool{"--rcfile": true, "--init-file": true}}
	zshOptionReading  = shellOptionReading{attachedO: true}
)

// shellInterpreters names the shells whose -c payload this guard recurses
// into with the same inspector, and how each reads its option words.
//
// sh gets both readings because it is a different shell on different
// machines: bash on macOS by default, zsh when /private/var/select/sh points
// there, dash on Debian. Inspecting a payload the actual sh would not run
// only ever refuses a call that fails anyway. dash reads -o the way bash
// does and rejects -O, so the bash reading covers it. See inspectBashDepth's
// wb#500 comment.
var shellInterpreters = map[string][]shellOptionReading{
	"bash": {bashOptionReading},
	"dash": {bashOptionReading},
	"sh":   {bashOptionReading, zshOptionReading},
	"zsh":  {zshOptionReading},
	"ksh":  {zshOptionReading},
}

// shellDashCPayloads reports every word that one of readings would run as the
// command string of a shell's -c flag. It returns none when the shell has no
// -c payload.
//
// The payload is not "the word after -c" (wb#500 final review, S2). The shell
// reads option words until the first word that is not one, and that word is
// the payload. Every shape below ran a marker payload on the real bash 3.2,
// sh, zsh 5.9, dash and ksh on macOS:
//
//   - -c anywhere among the option words, alone or inside a cluster (-lc, -ec,
//     -xc, -ceo pipefail), and +c as well.
//   - Option words after -c: -e, -x, +x, -o pipefail, +o errexit, and on bash
//     -O extglob.
//   - -- and a lone - end the option words, so the word after them is the
//     payload.
//
// Each o in a cluster takes a value; see shellOptionReading. Words after the
// payload become $0, $1, ... and never run. A word before -c that is not an
// option is a script file, so there is no payload: `bash script.sh -c x` runs
// script.sh.
func shellDashCPayloads(words []string, readings []shellOptionReading) []string {
	var payloads []string
	for _, reading := range readings {
		payload, ok := shellDashCPayload(words, reading)
		if ok && !containsWord(payloads, payload) {
			payloads = append(payloads, payload)
		}
	}
	return payloads
}

// shellDashCPayload is shellDashCPayloads for one reading.
func shellDashCPayload(words []string, reading shellOptionReading) (string, bool) {
	sawC := false
	for index := 1; index < len(words); index++ {
		word := words[index]
		switch {
		case word == "--" || word == "-":
			if sawC && index+1 < len(words) {
				return words[index+1], true
			}
			return "", false
		case strings.HasPrefix(word, "--"):
			if reading.longValues[word] {
				index++
			}
		case strings.HasPrefix(word, "-") || strings.HasPrefix(word, "+"):
			letters := word[1:]
		cluster:
			for position := 0; position < len(letters); position++ {
				switch letters[position] {
				case 'c':
					sawC = true
				case 'o':
					if reading.attachedO && position+1 < len(letters) {
						break cluster
					}
					index++
				case 'O':
					if reading.valueO {
						index++
					}
				}
			}
		default:
			if sawC {
				return word, true
			}
			return "", false
		}
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
// words before stripCommandPrefixes removed any leading assignment or
// transparent prefix (sudo, env, ...). inspectGh needs them to read an inline
// WB_AGENTGUARD_ALLOW_GH_PR_MERGE="<reason>" on this exact call, the only
// place that override is honoured (see gh.go).
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

// commandPrefix describes one word that runs the command written after it,
// so the guard has to read past it to reach the real program. It is either a
// shell reserved word or a wrapper program. The fields say which of the
// following words belong to the prefix rather than to the command it runs.
type commandPrefix struct {
	// keyword marks a shell reserved word. It has no options of its own, so
	// the very next word starts the command.
	keyword bool
	// shortValues are the option letters that take a value: the rest of
	// their word when any remains (-n10, -oL, -I{}), otherwise the next word
	// (-n 10, -o L, -I {}). Every other letter is a flag. The first word
	// that does not start with "-" ends the options, and so does "--".
	shortValues string
	// longValues are the long options that take the next word when written
	// without "=" (sudo --user alex).
	longValues map[string]bool
	// operands counts the positional words between the options and the
	// command, such as timeout's DURATION.
	operands int
	// exportsAssignments marks a prefix after which a VAR=value word still
	// reaches the program's environment: the shell keywords, time, env and
	// sudo. After any other wrapper, the shell or the wrapper tries to run
	// VAR=value itself as the program and fails ("nice: VAR=x: No such file
	// or directory"). Confirmed against bash 3.2 and zsh 5.9 on macOS.
	exportsAssignments bool
	// arguments, when set, replaces the option walk. It returns the command
	// words, or false when the invocation runs no command of the caller's.
	arguments func(arguments []string) ([]string, bool)
	// governed marks a prefix that runs the command through WB's governed
	// gateway, so the governed-validation gate must not refuse it again.
	governed bool
}

// transparentCommandPrefixes is the one table stripCommandPrefixes reads.
// Both walks use it: the program-name walk that every recogniser sees the
// program through, and the override walk inspectGh needs
// (leadingAssignmentValue). One table means the two can never drift apart
// (wb#500 second review, Nit 2).
//
// Each wrapper also lists its own options. A wrapper is only transparent once
// its options and their values are skipped too: `sudo -u alex gh pr merge 1`
// has to reach gh instead of stopping at "alex" (wb#500 final review, S3).
// The option letters are the union of the macOS (BSD) and GNU spellings, from
// each program's manual.
//
// The reader splits segments at ; && || | & newline ( ) { }. So a loop or
// conditional body reaches here as `do gh pr merge "$n"` or
// `then gh pr merge 1`, and the reserved words below are stripped so the body
// is inspected. A wrapper that is not in this table (ssh, watch, arch, script,
// ...) is not seen through.
var transparentCommandPrefixes = map[string]commandPrefix{
	"!":      {keyword: true, exportsAssignments: true},
	"if":     {keyword: true, exportsAssignments: true},
	"then":   {keyword: true, exportsAssignments: true},
	"else":   {keyword: true, exportsAssignments: true},
	"elif":   {keyword: true, exportsAssignments: true},
	"while":  {keyword: true, exportsAssignments: true},
	"until":  {keyword: true, exportsAssignments: true},
	"do":     {keyword: true, exportsAssignments: true},
	"coproc": {keyword: true, exportsAssignments: true},
	// time is a reserved word in bash (time -p) and in zsh. /usr/bin/time
	// takes -o file, and GNU's also takes -f format.
	"time": {shortValues: "fo", longValues: setOf("--format", "--output"), exportsAssignments: true},
	"sudo": {
		shortValues: "aCcDgpRrTtUu",
		longValues: setOf("--auth-type", "--chdir", "--chroot", "--close-from", "--command-timeout", "--group",
			"--host", "--login-class", "--other-user", "--prompt", "--role", "--type", "--user"),
		exportsAssignments: true,
	},
	// env -S's value is a whole command line that env splits itself. It is
	// skipped as a value here, not read as the command.
	"env":        {shortValues: "CLPSUau", longValues: setOf("--argv0", "--chdir", "--split-string", "--unset"), exportsAssignments: true},
	"nice":       {shortValues: "n", longValues: setOf("--adjustment")},
	"nohup":      {},
	"stdbuf":     {shortValues: "eio", longValues: setOf("--error", "--input", "--output")},
	"exec":       {shortValues: "a"},
	"command":    {},
	"builtin":    {},
	"noglob":     {},
	"nocorrect":  {},
	"timeout":    {shortValues: "ks", longValues: setOf("--kill-after", "--signal"), operands: 1},
	"caffeinate": {shortValues: "tw"},
	"xargs": {
		shortValues: "EIJLPRSadns",
		longValues:  setOf("--arg-file", "--delimiter", "--max-args", "--max-chars", "--max-procs", "--process-slot-var"),
	},
	"wb": {arguments: wbRunCommand, governed: true},
}

func setOf(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[word] = true
	}
	return set
}

// strippedCommand is one segment's words with every leading assignment and
// transparent prefix removed.
type strippedCommand struct {
	// Words start at the real program name.
	Words []string
	// Assignments are the VAR=value words that the shell, env or sudo really
	// puts into the program's environment. Those are the ones at the start of
	// the segment, or right after a prefix with exportsAssignments set.
	Assignments []string
	// Governed is set when a prefix runs the command through `wb run --`.
	Governed bool
}

// stripCommandPrefixes walks past leading assignments and the prefixes in
// transparentCommandPrefixes, in any order and any number
// (`if ! sudo -u alex nice -n 5 gh ...`), so every recogniser sees the real
// program name.
//
// It skips every VAR=value word on the way, even one that no shell would
// export (`nice VAR=x gh ...` fails, because nice tries to run "VAR=x"), so
// the program name never hides behind a word the guard misjudged. Only the
// exported ones are reported in Assignments, because only those can carry an
// override to the program; see leadingAssignmentValue.
func stripCommandPrefixes(words []string) strippedCommand {
	var stripped strippedCommand
	exported := true
	for len(words) > 0 {
		word := words[0]
		if isEnvironmentAssignment(word) {
			if exported {
				stripped.Assignments = append(stripped.Assignments, word)
			}
			words = words[1:]
			continue
		}
		prefix, ok := transparentCommandPrefixes[filepath.Base(word)]
		if !ok {
			break
		}
		switch {
		case prefix.arguments != nil:
			command, runs := prefix.arguments(words[1:])
			if !runs {
				stripped.Words = words
				return stripped
			}
			words = command
		case prefix.keyword:
			words = words[1:]
		default:
			words = skipPrefixOptions(words[1:], prefix)
		}
		exported = prefix.exportsAssignments
		stripped.Governed = stripped.Governed || prefix.governed
	}
	stripped.Words = words
	return stripped
}

// skipPrefixOptions drops a wrapper's own options, their values and its
// operands from arguments, the words after the wrapper's name, and returns
// the words of the command it runs. See commandPrefix for the rules.
func skipPrefixOptions(arguments []string, prefix commandPrefix) []string {
	for len(arguments) > 0 {
		word := arguments[0]
		if word == "--" {
			arguments = arguments[1:]
			break
		}
		if !strings.HasPrefix(word, "-") {
			break
		}
		arguments = arguments[1:]
		takesNext := false
		if strings.HasPrefix(word, "--") {
			takesNext = !strings.Contains(word, "=") && prefix.longValues[word]
		} else {
			takesNext = clusterTakesNextWord(word[1:], prefix.shortValues)
		}
		if takesNext && len(arguments) > 0 {
			arguments = arguments[1:]
		}
	}
	for range prefix.operands {
		if len(arguments) > 0 {
			arguments = arguments[1:]
		}
	}
	return arguments
}

// clusterTakesNextWord reports whether a getopt-style short-option cluster
// (the letters after "-") takes the next word as a value. It does when the
// cluster's first value-taking letter is also its last letter: the n in
// `-n 10`, or in `-in 10` when i is a flag. Letters after the value-taking
// letter are its value (`-n10`, `-oL`, `-I{}`), and nothing more is consumed.
func clusterTakesNextWord(letters, valueLetters string) bool {
	for position, letter := range letters {
		if strings.ContainsRune(valueLetters, letter) {
			return position == len(letters)-1
		}
	}
	return false
}

// wbRootNoValueFlags are wb's root flags that take no value, for
// cobraSubcommandIndex. Every other root flag (--projects-root, --filter,
// --org) takes one.
var wbRootNoValueFlags = map[string]bool{"--non-interactive": true, "--help": true, "-h": true}

// wbRunCommand returns the command that `wb [flags] run [flags] -- <command>`
// runs. It returns false for every other wb invocation, including
// `wb run <recipe>`, which runs no command of the caller's. wb itself is
// otherwise exempt from this guard (see inspectCommand), so this is the only
// wb shape that is unwrapped.
func wbRunCommand(arguments []string) ([]string, bool) {
	index := cobraSubcommandIndex(arguments, wbRootNoValueFlags)
	if index < 0 || arguments[index] != "run" {
		return nil, false
	}
	for position := index + 1; position < len(arguments); position++ {
		if arguments[position] == "--" {
			return arguments[position+1:], true
		}
	}
	return nil, false
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

// leadingAssignmentValue reports the value that an exported VAR=value word
// on words assigns to variable. Exported means written where the shell
// really puts it into the program's environment: at the start of the call
// (`VAR=value gh ...`), through env (`env -u X VAR=value gh ...`) or sudo, or
// after a shell keyword (`do VAR=value gh ...`); see stripCommandPrefixes.
// The last matching assignment wins, mirroring a shell's own "last one wins"
// for a repeated variable.
//
// This exists for inspectGh's escape hatch (wb#500 review, Should-fix 3). It
// is read from the words of the exact call it prefixes, never from the hook
// process's own ambient environment, which would silently cover every gh pr
// merge for the rest of a session instead of the one call an operator meant
// to allow. An assignment no shell would export (`nice VAR=value gh ...`,
// which fails because nice tries to run "VAR=value") is not honoured: the
// guard refuses that call and records nothing (wb#500 final review, Nit 3).
func leadingAssignmentValue(words []string, variable string) (string, bool) {
	value, found := "", false
	for _, assignment := range stripCommandPrefixes(words).Assignments {
		if name, assigned := splitAssignment(assignment); name == variable {
			value, found = assigned, true
		}
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
