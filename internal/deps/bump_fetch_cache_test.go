package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// fetchCacheFixture is a synthetic 4-repository npm fleet for full merge-mode
// bump campaigns:
//
//	@acme/provider (seed owner, never a wave target)
//	@acme/lib      (wave 1: provider 1.0.0 -> 2.0.0)
//	@acme/app      (wave 2: lib 1.0.0 -> 1.1.0)
//	@acme/bystander (fleet member with no relevant dependencies, never touched)
//
// Its bare remotes live at <root>/remotes/<owner>/<name>.git so the fake `gh`
// on PATH can resolve a repository slug to the exact remote it must observe
// and, on merge, mutate.
type fetchCacheFixture struct {
	root       string
	githubDir  string
	remotesDir string
	realGit    string
	fetchLog   string
	repos      []Repository
}

func newFetchCacheFixture(t *testing.T) fetchCacheFixture {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	fixture := fetchCacheFixture{
		root:       root,
		githubDir:  filepath.Join(root, "projects"),
		remotesDir: filepath.Join(root, "remotes"),
		realGit:    realGit,
		fetchLog:   filepath.Join(root, "fetch.log"),
	}
	seed := func(name, packageJSON string) Repository {
		return fixture.seedRepository(t, name, packageJSON)
	}
	fixture.repos = []Repository{
		seed("provider", "{\n  \"name\": \"@acme/provider\",\n  \"version\": \"1.0.0\"\n}\n"),
		seed("lib", "{\n  \"name\": \"@acme/lib\",\n  \"version\": \"1.0.0\",\n  \"dependencies\": {\n    \"@acme/provider\": \"1.0.0\"\n  }\n}\n"),
		seed("app", "{\n  \"name\": \"@acme/app\",\n  \"version\": \"1.0.0\",\n  \"dependencies\": {\n    \"@acme/lib\": \"1.0.0\"\n  }\n}\n"),
		seed("bystander", "{\n  \"name\": \"@acme/bystander\",\n  \"version\": \"1.0.0\"\n}\n"),
	}
	return fixture
}

func (fixture fetchCacheFixture) seedRepository(t *testing.T, name, packageJSON string) Repository {
	t.Helper()
	seed := filepath.Join(fixture.root, "seed-"+name)
	remote := filepath.Join(fixture.remotesDir, "acme", name+".git")
	canonical := filepath.Join(fixture.githubDir, "acme", name)
	writeTestFile(t, filepath.Join(seed, "package.json"), packageJSON)
	runTestGit(t, seed, "init", "-b", "main")
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, fixture.root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, fixture.root, "clone", remote, canonical)
	runTestGit(t, canonical, "config", "user.name", "WB Test")
	runTestGit(t, canonical, "config", "user.email", "wb@example.test")
	return Repository{Slug: "acme/" + name, Path: canonical, CloneURL: remote}
}

// installFetchCountingGit puts a `git` shim first on PATH that appends the
// working directory to the fetch log for every `git fetch` before delegating
// to the real git, so tests can assert exactly which canonical clones were
// fetched how many times.
func (fixture fetchCacheFixture) installFetchCountingGit(t *testing.T) {
	t.Helper()
	binDir := filepath.Join(fixture.root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(binDir, "git"), "#!/bin/sh\n"+
		"if [ \"$1\" = fetch ]; then\n"+
		"  pwd >> \"$WB_FETCH_LOG\"\n"+
		"fi\n"+
		"exec \"$WB_REAL_GIT\" \"$@\"\n")
	if err := os.Chmod(filepath.Join(binDir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_REAL_GIT", fixture.realGit)
	t.Setenv("WB_FETCH_LOG", fixture.fetchLog)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// installServerSideMergeGH puts a fake `gh` on PATH that models GitHub for a
// merge-mode campaign. Crucially, `gh pr merge` lands the exact matched head
// commit onto the bare remote's main directly — a server-side merge that no
// local clone and no local push observes, exactly the write pattern the
// fetch-cache invalidation rule exists for. A local-push model could not
// catch a stale-read bug here, because the campaign's own pushes only ever
// touch topic branches.
func (fixture fetchCacheFixture) installServerSideMergeGH(t *testing.T) {
	t.Helper()
	binDir := filepath.Join(fixture.root, "bin")
	stateDir := filepath.Join(fixture.root, "gh-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
git() { command "$WB_REAL_GIT" "$@"; }
remote_for_slug() {
  printf '%s/%s.git' "$WB_FAKE_REMOTES" "$1"
}
arg_after() {
  want="$1"; shift
  prev=""
  for a in "$@"; do
    if [ "$prev" = "$want" ]; then printf '%s' "$a"; return 0; fi
    prev="$a"
  done
  return 1
}
if [ "$1" = pr ] && [ "$2" = list ]; then
  exit 0
fi
if [ "$1" = pr ] && [ "$2" = create ]; then
  branch=$(arg_after --head "$@") || exit 2
  origin=$(git remote get-url origin)
  name=$(basename "$origin" .git)
  owner=$(basename "$(dirname "$origin")")
  printf '%s' "$branch" > "$WB_FAKE_GH_STATE/$owner-$name.branch"
  printf 'https://github.test/%s/%s/pull/1\n' "$owner" "$name"
  exit 0
fi
if [ "$1" = pr ] && [ "$2" = view ]; then
  slug=$(arg_after --repo "$@") || exit 2
  branch=$(cat "$WB_FAKE_GH_STATE/$(printf '%s' "$slug" | tr / -).branch") || exit 2
  head=$(git --git-dir="$(remote_for_slug "$slug")" rev-parse "refs/heads/$branch") || exit 2
  printf '{"headRefOid":"%s","baseRefName":"main"}\n' "$head"
  exit 0
fi
if [ "$1" = pr ] && [ "$2" = checks ]; then
  printf '%s\n' '[{"name":"CI","bucket":"pass","link":"https://ci.test/run"}]'
  exit 0
fi
if [ "$1" = pr ] && [ "$2" = merge ]; then
  url="$3"
  sha=$(arg_after --match-head-commit "$@") || exit 2
  slug=${url#https://github.test/}
  slug=${slug%/pull/*}
  remote=$(remote_for_slug "$slug")
  main=$(git --git-dir="$remote" rev-parse refs/heads/main) || exit 2
  base=$(git --git-dir="$remote" merge-base "$main" "$sha") || exit 2
  if [ "$base" != "$main" ]; then
    echo "candidate $sha is not fast-forward from main $main" >&2
    exit 1
  fi
  git --git-dir="$remote" update-ref refs/heads/main "$sha" || exit 2
  exit 0
fi
if [ "$1" = api ]; then
  case " $* " in
    *"/rules/branches/main?per_page=100"*)
      printf '%s\n' '[[]]'
      exit 0
      ;;
  esac
  ep="$2"
  slug=$(printf '%s' "$ep" | cut -d/ -f2-3)
  remote=$(remote_for_slug "$slug")
  case "$ep" in
    repos/*/git/ref/heads/main)
      sha=$(git --git-dir="$remote" rev-parse refs/heads/main) || exit 2
      printf '{"object":{"sha":"%s"}}\n' "$sha"
      exit 0
      ;;
    repos/*/compare/*)
      pair=${ep##*/compare/}
      target=${pair%%...*}
      candidate=${pair##*...}
      if [ "$candidate" = "$target" ]; then
        status=identical
        mergeBase="$target"
      else
        mergeBase=$(git --git-dir="$remote" merge-base "$target" "$candidate") || exit 2
        if [ "$mergeBase" = "$target" ]; then status=ahead; else status=diverged; fi
      fi
      printf '{"status":"%s","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' "$status" "$target" "$mergeBase"
      exit 0
      ;;
    repos/*/commits/*/check-runs*)
      printf '%s\n' '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":42}}]}'
      exit 0
      ;;
    repos/*/commits/*/status*)
      printf '%s\n' '{"total_count":0,"statuses":[]}'
      exit 0
      ;;
    repos/*/branches/main/protection/required_status_checks)
      printf '%s\n' '{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}'
      exit 0
      ;;
    repos/*/branches/main)
      printf '%s\n' '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}'
      exit 0
      ;;
  esac
fi
echo "unexpected gh args: $*" >&2
exit 2
`
	writeTestFile(t, filepath.Join(binDir, "gh"), script)
	if err := os.Chmod(filepath.Join(binDir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_FAKE_REMOTES", fixture.remotesDir)
	t.Setenv("WB_FAKE_GH_STATE", stateDir)
}

// remoteMainSelects reports whether the bare remote for owner/name currently
// selects dependency@version in its main package.json. It deliberately reads
// the REMOTE, never a local clone: after a fake server-side merge this is the
// only place the landed manifest exists.
func (fixture fetchCacheFixture) remoteMainSelects(slug, dependency, version string) (bool, error) {
	remote := filepath.Join(fixture.remotesDir, filepath.FromSlash(slug)+".git")
	command := exec.Command(fixture.realGit, "--git-dir", remote, "show", "main:package.json")
	output, err := command.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("read %s main package.json: %v: %s", slug, err, output)
	}
	return strings.Contains(string(output), fmt.Sprintf("%q: %q", dependency, version)), nil
}

func (fixture fetchCacheFixture) fetchCounts(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	contents, err := os.ReadFile(fixture.fetchLog)
	if os.IsNotExist(err) {
		return counts
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		relative, err := filepath.Rel(fixture.githubDir, line)
		if err != nil || strings.HasPrefix(relative, "..") {
			t.Fatalf("fetch outside the fleet's canonical clones: %q", line)
		}
		counts[filepath.ToSlash(relative)]++
	}
	return counts
}

func (fixture fetchCacheFixture) bumpOptions(fetchCache bool) BumpOptions {
	return BumpOptions{
		Ecosystem: EcosystemNPM,
		Options: Options{
			GitHubDir: fixture.githubDir, Ref: "main",
			// Parallel 1 with explicit authority keeps every pool — including
			// the read-only discovery pool — serial, so the fetch log is
			// deterministic and free of interleaved writes.
			Parallel: 1, ParallelExplicit: true,
			ValidationMode: ValidationModeNone,
			Merge:          true,
			Timeout:        2 * time.Minute,
			// The fake gh answers instantly; a tight poll keeps the two
			// stability observations plus rereads fast.
			CheckPollInterval: 5 * time.Millisecond,
		},
		PollInterval: time.Millisecond,
		RefreshAfter: 0,
		FetchCache:   fetchCache,
		LatestNpmVersion: func(_ context.Context, pkg string) (string, error) {
			switch pkg {
			case "@acme/provider":
				return "2.0.0", nil
			case "@acme/lib":
				landed, err := fixture.remoteMainSelects("acme/lib", "@acme/provider", "2.0.0")
				if err != nil {
					return "", err
				}
				if landed {
					return "1.1.0", nil
				}
				return "1.0.0", nil
			default:
				return "", fmt.Errorf("unexpected npm version lookup for %s", pkg)
			}
		},
		LatestNpmRelease: func(_ context.Context, pkg string) (PublishedGoRelease, error) {
			if pkg != "@acme/lib" {
				return PublishedGoRelease{}, fmt.Errorf("unexpected npm release lookup for %s", pkg)
			}
			landed, err := fixture.remoteMainSelects("acme/lib", "@acme/provider", "2.0.0")
			if err != nil {
				return PublishedGoRelease{}, err
			}
			if landed {
				return PublishedGoRelease{Version: "1.1.0", Requirements: map[string]string{"@acme/provider": "2.0.0"}}, nil
			}
			return PublishedGoRelease{Version: "1.0.0", Requirements: map[string]string{"@acme/provider": "1.0.0"}}, nil
		},
	}
}

func runFetchCacheCampaign(t *testing.T, fixture fetchCacheFixture, fetchCache bool) BumpReport {
	t.Helper()
	// Not t.Parallel(): this drives a real (non-DryRun) orchestrate.Run and
	// mutates process env (WB_HOME, PATH, shim state) via t.Setenv.
	t.Setenv(wbhome.EnvOverride, filepath.Join(fixture.root, ".wb"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(fixture.root, "state"))
	fixture.installServerSideMergeGH(t)
	fixture.installFetchCountingGit(t)
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		fixture.repos, fixture.bumpOptions(fetchCache))
	if err != nil {
		t.Fatalf("merge-mode campaign failed: %v\nreport: %+v", err, report)
	}
	if report.Status != "completed" || len(report.Waves) != 3 {
		t.Fatalf("report status = %q with %d waves, want completed in exactly 3 waves (2 mutation + 1 fixpoint): %+v", report.Status, len(report.Waves), report)
	}
	wave1, wave2 := report.Waves[0], report.Waves[1]
	if len(wave1.Repositories) != 1 || wave1.Repositories[0].Repository != "acme/lib" || !wave1.Repositories[0].Merged {
		t.Fatalf("wave 1 = %+v, want acme/lib merged", wave1.Repositories)
	}
	if len(wave2.Repositories) != 1 || wave2.Repositories[0].Repository != "acme/app" || !wave2.Repositories[0].Merged {
		t.Fatalf("wave 2 = %+v, want acme/app merged", wave2.Repositories)
	}
	// The merges were genuinely server-side: the landed manifests exist on the
	// bare remotes' main even though no local clone pushed to main.
	if landed, err := fixture.remoteMainSelects("acme/lib", "@acme/provider", "2.0.0"); err != nil || !landed {
		t.Fatalf("acme/lib main must select @acme/provider 2.0.0 after the server-side merge (landed=%v err=%v)", landed, err)
	}
	if landed, err := fixture.remoteMainSelects("acme/app", "@acme/lib", "1.1.0"); err != nil || !landed {
		t.Fatalf("acme/app main must select @acme/lib 1.1.0 after the server-side merge (landed=%v err=%v)", landed, err)
	}
	return report
}

// TestRunBumpFetchCacheMemoizesUntouchedRepositoriesAndInvalidatesMergedOnes
// drives a real two-mutation-wave merge campaign over a synthetic fleet with
// --fetch-cache semantics enabled and asserts exact per-repository fetch
// counts:
//
//   - repositories the campaign never touched (provider, bystander) are
//     fetched exactly once for the whole run instead of once per
//     EnsureCanonical;
//   - a repository the run merged is re-fetched by EVERY later discovery
//     (permanently un-memoizable), which is precisely what lets wave 2 observe
//     acme/lib's server-side-merged manifest and terminate in 3 waves instead
//     of spinning to --max-waves or cutting duplicate PRs from a stale base.
func TestRunBumpFetchCacheMemoizesUntouchedRepositoriesAndInvalidatesMergedOnes(t *testing.T) {
	fixture := newFetchCacheFixture(t)
	runFetchCacheCampaign(t, fixture, true)
	counts := fixture.fetchCounts(t)
	want := map[string]int{
		// wave-1 discovery only; memoized for wave-1 engine and waves 2-3.
		"acme/provider":  1,
		"acme/bystander": 1,
		// wave-1 discovery, then merged in wave 1: waves 2 and 3 must refetch.
		// The wave-1 engine hit (no second intra-wave fetch) is what keeps this
		// at 3 rather than 4.
		"acme/lib": 3,
		// wave-1 discovery (memoized through wave-2 discovery AND the wave-2
		// engine), then merged in wave 2: wave 3 must refetch.
		"acme/app": 2,
	}
	for repository, wanted := range want {
		if counts[repository] != wanted {
			t.Errorf("%s fetched %d times, want %d (all counts: %v)", repository, counts[repository], wanted, counts)
		}
	}
	if total := len(counts); total != len(want) {
		t.Errorf("fetches touched %d repositories, want %d: %v", total, len(want), counts)
	}
}

// TestRunBumpWithoutFetchCacheRefetchesEveryWave pins the opt-out default:
// without --fetch-cache the exact same campaign fetches every repository once
// per EnsureCanonical — one per repository per wave discovery plus one per
// wave-engine repository — today's behavior, unchanged.
func TestRunBumpWithoutFetchCacheRefetchesEveryWave(t *testing.T) {
	fixture := newFetchCacheFixture(t)
	runFetchCacheCampaign(t, fixture, false)
	counts := fixture.fetchCounts(t)
	want := map[string]int{
		"acme/provider":  3, // 3 wave discoveries
		"acme/bystander": 3, // 3 wave discoveries
		"acme/lib":       4, // 3 wave discoveries + wave-1 engine
		"acme/app":       4, // 3 wave discoveries + wave-2 engine
	}
	for repository, wanted := range want {
		if counts[repository] != wanted {
			t.Errorf("%s fetched %d times, want %d (all counts: %v)", repository, counts[repository], wanted, counts)
		}
	}
}

// TestRunExactSetFleetStillFetchesEveryCanonical pins that `deps set --fleet`
// keeps its unconditional engine fetch: it performs no graph discovery, so
// the EnsureCanonical fetch inside the wave engine is that operation's ONLY
// origin read and must never be memoized away. deps set has no --fetch-cache
// and threads no memo; every selected repository is fetched exactly once.
func TestRunExactSetFleetStillFetchesEveryCanonical(t *testing.T) {
	fixture := newFetchCacheFixture(t)
	t.Setenv(wbhome.EnvOverride, filepath.Join(fixture.root, ".wb"))
	fixture.installFetchCountingGit(t)
	target, err := ParseTarget("npm", "@acme/provider@2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), target, fixture.repos, Options{
		GitHubDir: fixture.githubDir, Ref: "main",
		Parallel: 1, ParallelExplicit: true,
		DryRun: true, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("deps set dry-run failed: %v\nreport: %+v", err, report)
	}
	counts := fixture.fetchCounts(t)
	for _, repository := range fixture.repos {
		if counts[repository.Slug] != 1 {
			t.Errorf("%s fetched %d times, want exactly 1 engine fetch (all counts: %v)", repository.Slug, counts[repository.Slug], counts)
		}
	}
}
