package agentguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// inspectAgentDispatch judges one subagent-dispatch tool call (Claude Code's
// `Agent`/`Task` tool). It never spawns a process: every check here reads, at
// most, a handful of small local files already sitting on disk.
//
// Three independent policies apply, checked cheapest and highest-confidence
// first. Each returns as soon as it finds something to refuse; a call that
// clears all three is allowed.
func inspectAgentDispatch(input toolInput, cwd, projectsRoot string) *finding {
	if result := inspectMissingModel(input); result != nil {
		return result
	}
	if result := inspectLiteralReportPath(input); result != nil {
		return result
	}
	if result := inspectDispatchIntoLiveClaim(input, cwd, projectsRoot); result != nil {
		return result
	}
	return nil
}

// inspectMissingModel denies a subagent dispatch that never names a model,
// because Claude Code inherits the parent's model silently when it is left
// out — precisely the compliance failure
// lesson:l49-subagent-dispatch-should-fail-before-an-unnamed-model-can-inherit
// describes: the rule lived only as prose
// (~/.claude/CLAUDE.md: "Name the model on every subagent dispatch — never let
// it inherit"), so nothing failed closed when the field was simply absent.
//
// A dispatch with no prompt at all (an empty tool_input, or a tool_input this
// guard could not decode) is not judged here: that is a malformed or unknown
// call, not a policy violation this rule owns, and the fail-open default
// applies.
func inspectMissingModel(input toolInput) *finding {
	if strings.TrimSpace(input.Prompt) == "" && strings.TrimSpace(input.Model) == "" {
		// Nothing about this call looks like a real dispatch (most likely an
		// unrecognised tool_input shape) — allow rather than guess.
		return nil
	}
	if strings.TrimSpace(input.Model) != "" {
		return nil
	}
	var message strings.Builder
	message.WriteString("This subagent dispatch names no `model`.\n\n")
	message.WriteString("rule: \"Name the model on every subagent dispatch — never let it inherit.\"\n")
	message.WriteString("(~/.claude/CLAUDE.md, Delegation; lesson l49-subagent-dispatch-should-fail-before-an-unnamed-model-can-inherit)\n\n")
	message.WriteString("An omitted model silently inherits the parent's, which is exactly what the\n")
	message.WriteString("rule forbids: a cheaper tier must be named explicitly, not assumed.\n\n")
	message.WriteString("Remedy: add an explicit model to this dispatch, e.g. model: \"sonnet\".\n")
	return &finding{Message: message.String()}
}

// literalReportPath matches a hand-written WB report path — the exact
// construct lesson:a-report-path-hand-written-into-a-brief-diverges-from-wb-home-on-the-target-host
// names: a brief assumes one fixed $HOME/WB_HOME layout, which is false on a
// host where WB_HOME is relocated (the lesson's own example:
// /home/ai/projects/.wb on the VM, not $HOME/.wb).
var literalReportPath = regexp.MustCompile(`(?:/Users/[^/\s]+/\.wb/reports|\$HOME/\.wb/reports|~/\.wb/reports)`)

// inspectLiteralReportPath denies a dispatch whose prompt hand-writes a
// report path instead of deriving it from the tool that owns the lifecycle it
// closes out.
func inspectLiteralReportPath(input toolInput) *finding {
	if !literalReportPath.MatchString(input.Prompt) {
		return nil
	}
	var message strings.Builder
	message.WriteString("This brief hand-writes a WB report path.\n\n")
	message.WriteString("rule: brief-report-path-from-owning-verb\n")
	message.WriteString("(lesson a-report-path-hand-written-into-a-brief-diverges-from-wb-home-on-the-target-host)\n\n")
	message.WriteString("A literal path assumes one fixed $HOME/WB_HOME layout. Briefs run on hosts\n")
	message.WriteString("where that assumption is false (WB_HOME is relocated, e.g. to\n")
	message.WriteString("/home/ai/projects/.wb on the VM) and the divergence is invisible until the\n")
	message.WriteString("report lands somewhere nobody is watching.\n\n")
	message.WriteString("Remedy: derive the report path from the tool that owns the lifecycle it\n")
	message.WriteString("closes out — `wb worktree log finalize --report` — never a literal string.\n")
	return &finding{Message: message.String()}
}

// repositoryPathPattern extracts an <owner>/<repository> pair from an
// absolute projects-root path named in a brief:
// /Users/alex/projects/<owner>/<repository>[/...] — the brief's own example
// host — generalised to any absolute `.../projects/<owner>/<repository>`
// path, because the fleet also runs a relocated WB_HOME (e.g.
// /home/ai/projects/... on the VM; see lesson
// a-report-path-hand-written-into-a-brief-diverges-from-wb-home-on-the-target-host)
// and a check that only recognised /Users/ would miss exactly the host this
// guard most needs to work on.
var repositoryPathPattern = regexp.MustCompile(`(?:/[A-Za-z0-9_.-]+)+/projects/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)`)

// repositorySlugNearWorktreeCreate extracts an <owner>/<repository> pair from
// an `owner/repo` token that appears on the same line as `wb worktree
// create`, per the brief's second detection shape.
var repositorySlugNearWorktreeCreate = regexp.MustCompile(`(?m)^.*\bwb worktree create\b.*$`)
var slugToken = regexp.MustCompile(`\b([A-Za-z0-9][A-Za-z0-9._-]*)/([A-Za-z0-9][A-Za-z0-9._-]*)\b`)

// candidateRepositories names every <owner>/<repository> a dispatch prompt
// mentions, via either detection shape the brief names.
func candidateRepositories(prompt string) [][2]string {
	seen := map[[2]string]bool{}
	var repositories [][2]string
	add := func(owner, name string) {
		key := [2]string{owner, name}
		if owner == "" || name == "" || seen[key] {
			return
		}
		seen[key] = true
		repositories = append(repositories, key)
	}
	for _, match := range repositoryPathPattern.FindAllStringSubmatch(prompt, -1) {
		add(match[1], match[2])
	}
	for _, line := range repositorySlugNearWorktreeCreate.FindAllString(prompt, -1) {
		for _, match := range slugToken.FindAllStringSubmatch(line, -1) {
			if match[1] == "wb" {
				continue
			}
			add(match[1], match[2])
		}
	}
	return repositories
}

// dispatchManifest is the subset of a worktree's local `.wb/local/
// manifest.yaml` this policy reads. See internal/worktrees for the writer.
type dispatchManifest struct {
	Repository string `yaml:"repository"`
	Worktree   string `yaml:"worktree"`
	EffortID   string `yaml:"effort_id"`
}

// inspectDispatchIntoLiveClaim denies a brief dispatched for a repository
// that another live WB claim already covers — the exact gap
// lesson:a-brief-was-dispatched-for-work-already-under-an-active-wb-claim
// records: a second brief for claimed work is caught today only if the
// implementing lane happens to search branches and PRs before creating a
// worktree; nothing at dispatch time checks.
//
// Detection is local-only and does not spawn a process: it lists the
// candidate repository's own `.worktrees/` directory (WB's default,
// repository-local worktree layout — see AGENTS.md §1) and reads each
// worktree's small `.wb/local/manifest.yaml`. A repository using WB's
// alternate shared-worktrees-root layout is not covered by this check; that
// is a scoped, reported limitation, not a silent gap; the failure mode is a
// missed refusal (fail open), never a wrongful one.
//
// "Another session" is approximated by working directory: if the call's own
// cwd already sits inside the claimed worktree, this is the same lane
// continuing its own work and is allowed. Otherwise the claim belongs to a
// context this dispatch is not part of, exactly the shape of the 2026-09-09
// incident the lesson records (the second brief's session was the founder's
// own fresh reproduction against main, not the lane that held the claim).
// This is a deliberate proxy, not a session-identity comparison: Claude
// Code's PreToolUse `session_id` and WB's own `wb_session_id` are different
// ID spaces with no documented mapping between them, so comparing them
// directly would be a guess, and this guard does not guess.
func inspectDispatchIntoLiveClaim(input toolInput, cwd, projectsRoot string) *finding {
	if projectsRoot == "" {
		return nil
	}
	callerDirectory, _ := absolutePath(cwd)
	for _, repository := range candidateRepositories(input.Prompt) {
		owner, name := repository[0], repository[1]
		worktreesDir := filepath.Join(projectsRoot, owner, name, ".worktrees")
		entries, err := os.ReadDir(worktreesDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			manifestPath := filepath.Join(worktreesDir, entry.Name(), ".wb", "local", "manifest.yaml")
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}
			var manifest dispatchManifest
			if yaml.Unmarshal(raw, &manifest) != nil {
				continue
			}
			if manifest.Repository != owner+"/"+name || manifest.EffortID == "" {
				continue
			}
			worktreePath, ok := absolutePath(manifest.Worktree)
			if !ok {
				continue
			}
			if callerDirectory != "" && withinDirectory(callerDirectory, worktreePath) {
				// The dispatching call is itself running from inside the
				// claimed worktree: this is that lane continuing its own
				// work, not a second, conflicting dispatch.
				continue
			}
			if !claimLive(projectsRoot, manifest.EffortID) {
				continue
			}
			return &finding{Message: liveClaimRefusal(projectsRoot, owner, name, manifest.EffortID, worktreePath)}
		}
	}
	return nil
}

// withinDirectory reports whether candidate is directory itself or nested
// inside it.
func withinDirectory(candidate, directory string) bool {
	if candidate == directory {
		return true
	}
	relative, err := filepath.Rel(directory, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// claimOwnerFile is the minimal shape this guard reads from a remote-state
// claim file (internal/remotestate.Claim), duplicated here rather than
// imported so this package keeps its own, independently auditable read path
// with no dependency beyond a YAML decode of a file already on disk.
type claimOwnerFile struct {
	Login   string `yaml:"login"`
	Machine string `yaml:"machine"`
}

// remoteConfigFile is the minimal shape this guard reads from
// ~/.config/wb/wb.yaml to find the local mirror of the claims store.
type remoteConfigFile struct {
	Remote struct {
		Provider string `yaml:"provider"`
		Repo     string `yaml:"repo"`
	} `yaml:"remote"`
}

// claimLive reports whether task's claim file still exists in the local
// mirror of the claims store. A claim file's existence in the store IS the
// claim (see internal/remotestate.Claim); releasing it deletes the file. A
// "hub" remote provider has no local mirror to read cheaply, and an
// unconfigured or unreadable remote config makes the answer unknown — both
// resolve to "not live", which is the fail-open direction for this guard.
func claimLive(projectsRoot, task string) bool {
	owner, name, ok := wbStateRepository()
	if !ok {
		return false
	}
	claimPath := filepath.Join(projectsRoot, owner, name, "claims", task+".yaml")
	info, err := os.Stat(claimPath)
	return err == nil && !info.IsDir()
}

// claimOwner reads the login/machine that hold task's claim, or "" if it
// cannot be read — a missing owner degrades the refusal message, it never
// blocks it.
func claimOwner(projectsRoot, task string) string {
	owner, name, ok := wbStateRepository()
	if !ok {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(projectsRoot, owner, name, "claims", task+".yaml"))
	if err != nil {
		return ""
	}
	var claim claimOwnerFile
	if yaml.Unmarshal(raw, &claim) != nil || claim.Login == "" || claim.Machine == "" {
		return ""
	}
	return claim.Login + "/" + claim.Machine
}

// wbStateRepository reads the configured remote-state repository from
// ~/.config/wb/wb.yaml (or $XDG_CONFIG_HOME/wb/wb.yaml), the same file
// internal/remotestate reads for `wb worktree create`'s own claim. Only the
// "git" provider has a local clone this guard can read without a network
// call; "hub" and an absent/invalid config both report not-found.
func wbStateRepository() (owner, name string, ok bool) {
	path := wbConfigPath()
	if path == "" {
		return "", "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	var config remoteConfigFile
	if yaml.Unmarshal(raw, &config) != nil {
		return "", "", false
	}
	if config.Remote.Provider != "git" || config.Remote.Repo == "" {
		return "", "", false
	}
	owner, name, found := strings.Cut(config.Remote.Repo, "/")
	if !found || owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
}

func wbConfigPath() string {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "wb", "wb.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "wb", "wb.yaml")
}

func liveClaimRefusal(projectsRoot, owner, name, task, worktreePath string) string {
	slug := owner + "/" + name
	var message strings.Builder
	message.WriteString("This dispatch targets " + slug + ", which already has a live WB claim.\n\n")
	message.WriteString("rule: brief-consults-live-claims-before-dispatch\n")
	message.WriteString("(lesson a-brief-was-dispatched-for-work-already-under-an-active-wb-claim)\n\n")
	message.WriteString("task: " + task + "\n")
	if holder := claimOwner(projectsRoot, task); holder != "" {
		message.WriteString("owner: " + holder + "\n")
	}
	message.WriteString("worktree: " + worktreePath + "\n\n")
	message.WriteString("Nothing at dispatch time otherwise checks whether this change is already\n")
	message.WriteString("claimed under a different task slug; a second brief for it produces a\n")
	message.WriteString("second, conflicting worktree/PR.\n\n")
	message.WriteString("Remedy: read the claim (`wb worktree list --filter " + slug + "`), confirm with\n")
	message.WriteString("whoever holds it, and fold this work into that lane instead of a new one.\n")
	return message.String()
}
