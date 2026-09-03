package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

// fakeGit answers the Git port without a repository, and records the excludes
// it was asked to write so the "never a tracked .gitignore" rule is provable.
type fakeGit struct {
	hash     string
	dirty    bool
	hashErr  error
	tracked  map[string][]string
	excludes map[string][]string
}

func newFakeGit() *fakeGit {
	return &fakeGit{hash: "treehash0123456789", dirty: true, tracked: map[string][]string{}, excludes: map[string][]string{}}
}

func (git *fakeGit) ContentHash(_ context.Context, dir string) (string, bool, error) {
	if git.hashErr != nil {
		return "", false, git.hashErr
	}
	return git.hash, git.dirty, nil
}

func (git *fakeGit) TrackedChanges(_ context.Context, dir string) ([]string, error) {
	return git.tracked[dir], nil
}

func (git *fakeGit) ExcludePath(_ context.Context, dir, pattern string) error {
	git.excludes[dir] = append(git.excludes[dir], pattern)
	return nil
}

func (git *fakeGit) ExcludedPatterns(_ context.Context, dir string) ([]string, error) {
	return git.excludes[dir], nil
}

// fakeNode records every build and link, and can fail the frozen install so
// the lockfile-baseline requirement is provable.
type fakeNode struct {
	installErr   map[string]error
	unlinkErr    map[string]error
	order        []string
	installed    []string
	builds       int
	buildErr     error
	dist         string
	linked       map[string]string
	unlinked     []string
	previousReal string
}

func newFakeNode() *fakeNode {
	return &fakeNode{installErr: map[string]error{}, dist: "/cache/dist", linked: map[string]string{}}
}

func (node *fakeNode) FrozenInstall(_ context.Context, dir string) error {
	if err := node.installErr[dir]; err != nil {
		return err
	}
	node.installed = append(node.installed, dir)
	node.order = append(node.order, "install "+dir)
	return nil
}

func (node *fakeNode) Build(_ context.Context, libraryDir, packageDir string) (string, error) {
	if node.buildErr != nil {
		return "", node.buildErr
	}
	node.builds++
	return node.dist, nil
}

func (node *fakeNode) Link(_ context.Context, consumerDir, packageName, dist string) (string, error) {
	node.linked[consumerDir+" "+packageName] = dist
	node.order = append(node.order, "link "+consumerDir+" "+packageName)
	return node.previousReal, nil
}

func (node *fakeNode) Unlink(_ context.Context, consumerDir, packageName string) error {
	if err := node.unlinkErr[consumerDir+" "+packageName]; err != nil {
		return err
	}
	node.unlinked = append(node.unlinked, consumerDir+" "+packageName)
	return nil
}

type fakeVerifier struct {
	linked   map[string]VerificationRun
	baseline map[string]VerificationRun
	envSeen  map[string][]string
}

func newFakeVerifier() *fakeVerifier {
	return &fakeVerifier{linked: map[string]VerificationRun{}, baseline: map[string]VerificationRun{}, envSeen: map[string][]string{}}
}

func (verifier *fakeVerifier) Verify(_ context.Context, dir string, env []string) (VerificationRun, error) {
	verifier.envSeen[dir] = env
	if run, ok := verifier.linked[dir]; ok {
		return run, nil
	}
	return VerificationRun{Passed: true, Command: "go test -p 1 ./..."}, nil
}

func (verifier *fakeVerifier) BuildAndVet(_ context.Context, dir string) (VerificationRun, error) {
	if run, ok := verifier.baseline[dir]; ok {
		return run, nil
	}
	return VerificationRun{Passed: true, Command: "go build ./...; go vet ./..."}, nil
}

func writeTree(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for name, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type fixture struct {
	engine   *Engine
	store    *streams.Store
	git      *fakeGit
	node     *fakeNode
	verifier *fakeVerifier
	library  string
	consumer string
}

func newFixture(t *testing.T, libraryFiles, consumerFiles map[string]string) fixture {
	t.Helper()
	base := t.TempDir()
	library := writeTree(t, filepath.Join(base, "library"), libraryFiles)
	consumer := writeTree(t, filepath.Join(base, "consumer"), consumerFiles)
	store := streams.OpenAt(filepath.Join(base, "wb-home", "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "fixture",
		Members: []streams.Member{
			{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: library, Branch: "stream/fixture", Base: "main"},
			{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: consumer, Branch: "stream/fixture", Base: "main"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	git, node, verifier := newFakeGit(), newFakeNode(), newFakeVerifier()
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	return fixture{
		engine: &Engine{
			Store: store, Git: git, Node: node, Verifier: verifier,
			CacheRoot: filepath.Join(base, "cache"),
			Now:       func() time.Time { return fixed },
		},
		store: store, git: git, node: node, verifier: verifier,
		library: library, consumer: consumer,
	}
}

const goLibraryModule = "module github.com/acme/library/backend\n\ngo 1.27\n"

// AC: go-consumer-builds-against-the-library-worktree — the go.work at the
// consumer worktree root carries `use` entries for every module in the consumer
// worktree AND the library; go.work and go.work.sum are both excluded; go.mod
// is unchanged.
func TestGoConsumerGetsAWorkspaceNamingEveryModuleAndTheLibrary(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{
			"backend/go.mod":    "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
			"tools/lint/go.mod": "module github.com/acme/app/tools/lint\n\ngo 1.26\n",
		})
	goModBefore := readFile(t, filepath.Join(fixture.consumer, "backend", "go.mod"))

	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if result.Failed() {
		t.Fatalf("result = %#v", result.Consumers)
	}
	workspace := readFile(t, filepath.Join(fixture.consumer, "go.work"))
	for _, want := range []string{"./backend", "./tools/lint", filepath.ToSlash(filepath.Join(fixture.library, "backend"))} {
		if !strings.Contains(workspace, want) {
			t.Errorf("go.work does not use %q:\n%s", want, workspace)
		}
	}
	if !strings.Contains(workspace, "go 1.27") {
		t.Errorf("go.work does not carry the consumer's own go directive:\n%s", workspace)
	}
	excluded := fixture.git.excludes[fixture.consumer]
	if !containsAll(excluded, "/go.work", "/go.work.sum") {
		t.Errorf("excluded = %v, want both go.work and go.work.sum", excluded)
	}
	if after := readFile(t, filepath.Join(fixture.consumer, "backend", "go.mod")); after != goModBefore {
		t.Errorf("go.mod changed:\n%s", after)
	}
	if strings.Contains(goModBefore, "replace") {
		t.Error("fixture is wrong: the consumer already had a replace directive")
	}

	// The link is recorded in stream state with the version it replaced.
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 1 {
		t.Fatalf("recorded links = %#v, want one", member.Links)
	}
	link := member.Links[0]
	if link.Mechanism != streams.MechanismGoWork || link.PreviousVersion != "v0.4.0" || link.ContentHash != fixture.git.hash {
		t.Fatalf("link = %#v", link)
	}
}

// REQ: local-link-discovers-what-the-library-publishes — a consumer that
// declares none of the discovered identities is reported and skipped, never
// linked to something it does not use.
func TestAConsumerThatDoesNotDependOnTheLibraryIsSkipped(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Consumers) != 1 || !result.Consumers[0].Skipped {
		t.Fatalf("consumers = %#v, want one skipped", result.Consumers)
	}
	if !strings.Contains(result.Consumers[0].Reason, "github.com/acme/library/backend") {
		t.Errorf("reason does not name what was looked for: %s", result.Consumers[0].Reason)
	}
	if _, err := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(err) {
		t.Error("a skipped consumer was linked anyway")
	}
}

func TestALibraryPublishingNothingIsRefusedRatherThanGuessed(t *testing.T) {
	fixture := newFixture(t, map[string]string{"README.md": "no manifests\n"}, map[string]string{})
	_, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err == nil || !strings.Contains(err.Error(), "publishes no discoverable") {
		t.Fatalf("error = %v, want a refusal to guess", err)
	}
}

// AC: npm-consumer-links-without-tracked-config — the library is built once
// with the repository's own build target and linked from its dist; every
// manifest stays byte-identical.
func TestNpmConsumerLinksFromABuiltDistWithoutTouchingTrackedConfig(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`},
		map[string]string{
			"package.json":        `{"name":"app","dependencies":{"@acme/core":"^1.0.0"}}`,
			"pnpm-workspace.yaml": "packages:\n  - apps/*\n",
			"pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
		})
	manifestBefore := readFile(t, filepath.Join(fixture.consumer, "package.json"))
	workspaceBefore := readFile(t, filepath.Join(fixture.consumer, "pnpm-workspace.yaml"))

	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed() {
		t.Fatalf("result = %#v", result.Consumers)
	}
	if fixture.node.builds != 1 {
		t.Errorf("builds = %d, want exactly one", fixture.node.builds)
	}
	if len(fixture.node.installed) != 1 || fixture.node.installed[0] != fixture.consumer {
		t.Errorf("frozen installs = %v, want the unlinked consumer proved once", fixture.node.installed)
	}
	if fixture.node.linked[fixture.consumer+" @acme/core"] != fixture.node.dist {
		t.Errorf("linked = %v, want the built dist", fixture.node.linked)
	}
	if readFile(t, filepath.Join(fixture.consumer, "package.json")) != manifestBefore {
		t.Error("package.json is no longer byte-identical to its committed contents")
	}
	if readFile(t, filepath.Join(fixture.consumer, "pnpm-workspace.yaml")) != workspaceBefore {
		t.Error("pnpm-workspace.yaml is no longer byte-identical to its committed contents")
	}
	for _, forbidden := range []string{"overrides", "workspace:", "link:"} {
		if strings.Contains(manifestBefore+workspaceBefore, forbidden) {
			t.Errorf("tracked config contains %q", forbidden)
		}
	}
}

// REQ: npm-link-preserves-a-frozen-lockfile-baseline — a consumer whose frozen
// install fails is never linked, so a link cannot mask a lockfile mismatch.
func TestNpmLinkRefusesWhenTheFrozenInstallFails(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`},
		map[string]string{
			"package.json":   `{"name":"app","dependencies":{"@acme/core":"^1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})
	fixture.node.installErr[fixture.consumer] = errors.New("lockfile is out of date")
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() {
		t.Fatal("a failed frozen install did not fail the link")
	}
	if fixture.node.builds != 0 || len(fixture.node.linked) != 0 {
		t.Errorf("the library was built or linked despite an unproved baseline: builds=%d linked=%v", fixture.node.builds, fixture.node.linked)
	}
}

// AC: verify-reports-every-consumer-single-worker — both consumers are
// verified, the failure is attributed to its consumer, and the passing consumer
// is still reported.
func TestVerifyReportsEveryConsumerAndDoesNotStopAtTheFirstFailure(t *testing.T) {
	base := t.TempDir()
	library := writeTree(t, filepath.Join(base, "library"), map[string]string{"backend/go.mod": goLibraryModule})
	consumerModule := "module github.com/acme/%s/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"
	failing := writeTree(t, filepath.Join(base, "failing"), map[string]string{"backend/go.mod": strings.Replace(consumerModule, "%s", "failing", 1)})
	passing := writeTree(t, filepath.Join(base, "passing"), map[string]string{"backend/go.mod": strings.Replace(consumerModule, "%s", "passing", 1)})
	store := streams.OpenAt(filepath.Join(base, "wb-home", "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "verify",
		Members: []streams.Member{
			{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: library},
			{Repository: "acme/failing", Role: streams.RoleConsumer, Worktree: failing},
			{Repository: "acme/passing", Role: streams.RoleConsumer, Worktree: passing},
		},
	}); err != nil {
		t.Fatal(err)
	}
	git, verifier := newFakeGit(), newFakeVerifier()
	verifier.linked[failing] = VerificationRun{Passed: false, Command: "go test -p 1 ./...", Details: []string{"backend test: compilation failed"}}
	engine := &Engine{Store: store, Git: git, Node: newFakeNode(), Verifier: verifier, CacheRoot: filepath.Join(base, "cache")}

	result, err := engine.Run(context.Background(), Options{
		Library: library, Consumers: []string{failing, passing}, Verify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Consumers) != 2 {
		t.Fatalf("consumers = %#v, want both reported", result.Consumers)
	}
	byPath := map[string]ConsumerResult{}
	for _, consumer := range result.Consumers {
		byPath[consumer.Consumer] = consumer
	}
	if byPath[failing].Verification == nil || byPath[failing].Verification.Passed {
		t.Fatalf("the failing consumer = %#v, want a failed verification", byPath[failing].Verification)
	}
	if byPath[passing].Verification == nil || !byPath[passing].Verification.Passed {
		t.Fatalf("the passing consumer = %#v, want it reported as passing", byPath[passing].Verification)
	}
	statement := byPath[passing].Verification.Statement
	if !strings.Contains(statement, "verified against unpublished") || !strings.Contains(statement, git.hash) || !strings.Contains(statement, "(dirty)") {
		t.Errorf("statement = %q, want the unpublished/content-hash/dirty sentence", statement)
	}
	if len(byPath[passing].Verification.ActiveLinks) == 0 {
		t.Error("the verification did not print its active links")
	}
	if !strings.Contains(byPath[passing].Verification.ActiveLinks[0], "replaces v0.4.0") {
		t.Errorf("active link does not name the published version it replaced: %q", byPath[passing].Verification.ActiveLinks[0])
	}
	if byPath[passing].Verification.PublishedBaseline.Command == "" {
		t.Error("the GOWORK=off pre-landing check did not run")
	}
	env := verifier.envSeen[passing]
	if !containsAll(env, "NX_DAEMON=false", "NX_SKIP_NX_CACHE=true") {
		t.Errorf("verification env = %v, want the single-worker Node environment", env)
	}
}

// AC: undo-restores-published-versions — undo succeeds without reading the
// removed library worktree, leaves no go.work behind, and clears the record.
func TestUndoRestoresPublishedVersionsAfterTheLibraryIsGone(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(fixture.library); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Failed() {
		t.Fatalf("undo reported errors: %#v", result.Consumers)
	}
	if _, err := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(err) {
		t.Errorf("go.work survived undo: %v", err)
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 0 {
		t.Fatalf("links survived undo: %#v", member.Links)
	}
}

func TestUndoOnAConsumerWithNoRecordedLinkIsReportedNotFailed(t *testing.T) {
	fixture := newFixture(t, map[string]string{"backend/go.mod": goLibraryModule}, map[string]string{})
	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Consumers[0].Skipped || !strings.Contains(result.Consumers[0].Reason, "nothing to undo") {
		t.Fatalf("consumers = %#v", result.Consumers)
	}
}

// Re-linking after the library moves must replace the record rather than append
// a second one, and must keep the ORIGINAL published version — that is what
// undo has to restore.
func TestRelinkingKeepsTheOriginalPublishedVersion(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.git.hash = "movedhash9876543210"
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 1 {
		t.Fatalf("links = %#v, want the record replaced rather than appended", member.Links)
	}
	if member.Links[0].ContentHash != "movedhash9876543210" {
		t.Errorf("content hash = %q, want the moved tree", member.Links[0].ContentHash)
	}
	if member.Links[0].PreviousVersion != "v0.4.0" {
		t.Errorf("previous version = %q, want the version that was published before any link existed", member.Links[0].PreviousVersion)
	}
}

// A link that changes a tracked file is a defect, and the verb says so rather
// than reporting success.
func TestLinkingThatChangesATrackedFileIsReportedAsAFailure(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	fixture.git.tracked[fixture.consumer] = nil
	original := fixture.git.TrackedChanges
	_ = original
	// The second read reports a tracked change the first did not, exactly as a
	// manifest-mutating link mechanism would.
	fixture.engine.Git = &trackedChangeInjector{fakeGit: fixture.git, after: []string{"package.json"}}
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() {
		t.Fatal("a tracked-file change was reported as success")
	}
	if !strings.Contains(strings.Join(result.Consumers[0].Errors, " "), "package.json") {
		t.Errorf("errors = %v, want the tracked file named", result.Consumers[0].Errors)
	}
}

type trackedChangeInjector struct {
	*fakeGit
	after []string
	reads int
}

func (git *trackedChangeInjector) TrackedChanges(ctx context.Context, dir string) ([]string, error) {
	git.reads++
	if git.reads == 1 {
		return nil, nil
	}
	return git.after, nil
}

func TestPlanStatesTheChecksBeforeTheyRun(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer}, Verify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(result.Plan, " | ")
	for _, want := range []string{"discover the library's published identities", "excluded go.work", "single-worker", "GOWORK=off"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan does not state %q: %s", want, plan)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func containsAll(values []string, wanted ...string) bool {
	present := map[string]bool{}
	for _, value := range values {
		present[value] = true
	}
	for _, want := range wanted {
		if !present[want] {
			return false
		}
	}
	return true
}

// MF-1. The record is written BEFORE the filesystem changes, so a record that
// cannot be written leaves nothing on disk to strand the worktree.
//
// Derived from the reviewer's probe A, which showed `go.work` written with
// zero links recorded and `--undo` reporting "nothing to undo".
func TestNoLinkIsWrittenWhenTheRecordCannotBe(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})

	directory := fixture.store.Dir("fixture")
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })

	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err == nil && !result.Failed() {
		t.Fatal("an unwritable record reported success")
	}
	if _, statErr := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(statErr) {
		t.Fatalf("go.work was written even though the link could not be recorded: %v", statErr)
	}
}

// MF-2. `--undo` clears a `go.work` that stream state has no record of, so the
// command the merge guard names can actually satisfy the guard.
func TestUndoRemovesAnUnrecordedGoWork(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	workspace := filepath.Join(fixture.consumer, "go.work")
	if err := os.WriteFile(workspace, []byte("go 1.27\n\nuse (\n\t./backend\n\t/elsewhere/library\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The guard fires and names exactly this command.
	links, err := HasLiveLink(fixture.store, fixture.consumer)
	if err != nil || len(links) != 1 {
		t.Fatalf("links = %#v, err = %v; want the go.work signal", links, err)
	}

	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Failed() {
		t.Fatalf("undo reported errors: %#v", result.Consumers)
	}
	if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("the unrecorded go.work survived --undo: %v", statErr)
	}
	// The guard is now satisfied — which is what makes the refusal's named
	// command a real next step rather than a dead end.
	after, err := HasLiveLink(fixture.store, fixture.consumer)
	if err != nil || len(after) != 0 {
		t.Fatalf("the guard still fires after --undo: %#v (err %v)", after, err)
	}
}

// MF-3. A failed removal KEEPS its record, so the guard stays closed and
// `stream end` keeps refusing while the artefact is still on disk.
func TestUndoKeepsTheRecordWhenRemovalFails(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`},
		map[string]string{
			"package.json":   `{"name":"app","dependencies":{"@acme/core":"^1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.node.unlinkErr = map[string]error{
		fixture.consumer + " @acme/core": errors.New("EACCES: node_modules is read-only"),
	}

	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() {
		t.Fatal("a failed removal was reported as a successful undo")
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 1 {
		t.Fatalf("links = %#v; a failed removal must keep its record so the guard stays closed", member.Links)
	}
	// The merge guard must still refuse.
	links, err := HasLiveLink(fixture.store, fixture.consumer)
	if err != nil || len(links) == 0 {
		t.Fatalf("the guard stopped firing while the link is still live: %#v (err %v)", links, err)
	}
}

// MF-5. The frozen install proves the UNLINKED tree, so it runs once per
// consumer regardless of how many identities that consumer declares.
func TestFrozenInstallRunsOncePerConsumerNotPerIdentity(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{
			"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`,
			"libs/ui/package.json":   `{"name":"@acme/ui","version":"1.0.0"}`,
		},
		map[string]string{
			"package.json":   `{"name":"app","dependencies":{"@acme/core":"^1.0.0","@acme/ui":"^1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed() {
		t.Fatalf("result = %#v", result.Consumers)
	}
	if len(fixture.node.installed) != 1 {
		t.Fatalf("frozen installs = %v, want exactly one against the unlinked tree", fixture.node.installed)
	}
	if len(fixture.node.linked) != 2 {
		t.Fatalf("linked = %v, want both identities linked", fixture.node.linked)
	}
	// The single install must precede every link.
	if fixture.node.order[0] != "install "+fixture.consumer {
		t.Fatalf("order = %v, want the frozen install first", fixture.node.order)
	}
}

// MF-7. A consumer no open stream holds is REFUSED before anything is written:
// an unrecorded link cannot be undone and the guard's state signal cannot see
// it.
func TestALinkThatCannotBeRecordedIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	base := t.TempDir()
	library := writeTree(t, filepath.Join(base, "library"), map[string]string{"backend/go.mod": goLibraryModule})
	consumer := writeTree(t, filepath.Join(base, "consumer"), map[string]string{
		"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
	})
	store := streams.OpenAt(filepath.Join(base, "wb-home", "streams"))
	engine := &Engine{Store: store, Git: newFakeGit(), Node: newFakeNode(), CacheRoot: filepath.Join(base, "cache")}

	_, err := engine.Run(context.Background(), Options{Library: library, Consumers: []string{consumer}})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalNotRecordable {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalNotRecordable)
	}
	if !strings.Contains(strings.Join(refusal.Sanctioned, " "), "wb stream join") {
		t.Errorf("refusal does not name how to make the consumer recordable: %v", refusal.Sanctioned)
	}
	if _, statErr := os.Stat(filepath.Join(consumer, "go.work")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused link still wrote go.work: %v", statErr)
	}
}

// MF (round 2). Membership is resolved per CONSUMER, not from the library.
//
// The reviewer's probe R2-F inverted: a stream holds the library and one app,
// and a THIRD worktree no member names is linked. Resolving from the library
// made this look recordable, so go.work was written, nothing was recorded, and
// the verb exited 0 — the same un-undoable link round 1 rejected.
func TestAConsumerOutsideTheStreamIsRefusedEvenWhenTheStreamHoldsTheLibrary(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})

	// A third worktree that is a real consumer but no stream member.
	outsider := writeTree(t, filepath.Join(t.TempDir(), "outsider"), map[string]string{
		"backend/go.mod": "module github.com/acme/outsider/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
	})

	_, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{outsider},
	})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalNotRecordable {
		t.Fatalf("error = %v, want a %s refusal even though the stream holds the library", err, RefusalNotRecordable)
	}
	if !strings.Contains(refusal.Message, outsider) {
		t.Errorf("refusal does not name the unrecordable consumer: %s", refusal.Message)
	}
	if _, statErr := os.Stat(filepath.Join(outsider, "go.work")); !os.IsNotExist(statErr) {
		t.Fatalf("go.work was written into a worktree no stream records: %v", statErr)
	}
}

// All fences run before the first side effect: one unrecordable consumer stops
// the whole invocation, so a recordable sibling is not half-linked.
func TestOneUnrecordableConsumerLinksNothingAtAll(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	outsider := writeTree(t, filepath.Join(t.TempDir(), "outsider"), map[string]string{
		"backend/go.mod": "module github.com/acme/outsider/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
	})

	_, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer, outsider},
	})
	if _, refused := Refused(err); !refused {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(statErr) {
		t.Fatalf("the recordable consumer was linked despite the refusal: %v", statErr)
	}
}

// recordLinks refuses rather than silently writing nothing when its update
// matches no member — the second half of the same defect.
func TestRecordLinksFailsWhenItMatchesNoMember(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	err := fixture.engine.recordLinks("fixture", filepath.Join(t.TempDir(), "not-a-member"), []streams.Link{
		{Library: fixture.library, Mechanism: streams.MechanismGoWork, Identity: "github.com/acme/library/backend"},
	})
	if err == nil {
		t.Fatal("recording against a path no member names reported success")
	}
	if !strings.Contains(err.Error(), "no member at") {
		t.Errorf("error = %v, want it to say the stream has no such member", err)
	}
	if err := fixture.engine.recordLinks("", fixture.consumer, []streams.Link{{Identity: "x"}}); err == nil {
		t.Fatal("recording with no stream reported success")
	}
}

// SHOULD-FIX (d). A consumer that was skipped is not verified, so it must not
// be told a verifier was unavailable for a run it was never part of.
func TestSkippedConsumersAreNotToldTheVerifierWasUnavailable(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	fixture.engine.Verifier = nil
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer}, Verify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Consumers) != 1 || !result.Consumers[0].Skipped {
		t.Fatalf("consumers = %#v, want the one consumer skipped", result.Consumers)
	}
	if len(result.Consumers[0].Errors) != 0 {
		t.Fatalf("a skipped consumer was given verification errors: %v", result.Consumers[0].Errors)
	}
}
