package locallink

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lgCovWriteFile is a tiny helper so the scenarios below stay readable.
func lgCovWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lgCovStagedConsumer prepares a consumer carrying a valid applied-link marker
// naming a stage directory, which is the state every Unlink scenario starts
// from.
func lgCovStagedConsumer(t *testing.T, packageName string) (consumer, target, marker, stage string) {
	t.Helper()
	consumer = t.TempDir()
	target = filepath.Join(consumer, "node_modules", filepath.FromSlash(packageName))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	marker = linkAppliedMarkerPath(consumer, packageName)
	stage = filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+wbStageSuffix)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	lgCovWriteFile(t, marker, stage+"\n")
	return consumer, target, marker, stage
}

func lgCovDist(t *testing.T, name, version string) string {
	t.Helper()
	dist := t.TempDir()
	lgCovWriteFile(t, filepath.Join(dist, "package.json"), `{"name":"`+name+`","version":"`+version+`"}`)
	return dist
}

// Link's failure surface must be reported, never half-applied silently.
func TestLgCovLinkFailurePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash"}

	t.Run("node_modules cannot be created", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovWriteFile(t, filepath.Join(consumer, "node_modules"), "not a directory\n")
		_, err := node.Link(ctx, consumer, "@acme/core", lgCovDist(t, "@acme/core", "1.1.0-dev"))
		if err == nil || !strings.Contains(err.Error(), "create "+filepath.Join(consumer, "node_modules", "@acme")) {
			t.Fatalf("error = %v, want the directory-creation failure", err)
		}
	})

	t.Run("a prior link that cannot be restored stops the refresh", func(t *testing.T) {
		t.Parallel()
		consumer, _, marker, _ := lgCovStagedConsumer(t, "@acme/core")
		lgCovWriteFile(t, marker, filepath.Join(consumer, "node_modules", ".bogus.wb-locallink-stage")+"\n")
		_, err := node.Link(ctx, consumer, "@acme/core", lgCovDist(t, "@acme/core", "1.1.0-dev"))
		if err == nil || !strings.Contains(err.Error(), "restore the prior local link before refreshing") {
			t.Fatalf("error = %v, want the prior-link restore refusal", err)
		}
	})

	t.Run("an unreadable target is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		if err := os.MkdirAll(filepath.Join(consumer, "node_modules", "@acme"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A final path component longer than NAME_MAX makes Lstat fail with
		// ENAMETOOLONG — neither "absent" nor a state Link may guess about.
		packageName := "@acme/" + strings.Repeat("x", 300)
		_, err := node.Link(ctx, consumer, packageName, lgCovDist(t, "@acme/core", "1.1.0-dev"))
		if err == nil || !strings.Contains(err.Error(), "inspect ") {
			t.Fatalf("error = %v, want the unreadable-target refusal", err)
		}
	})

	t.Run("a dist that is not a real directory is refused", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		_, err := node.Link(ctx, consumer, "@acme/core", filepath.Join(consumer, "missing-dist"))
		if err == nil || !strings.Contains(err.Error(), "stage @acme/core in the consumer's installed peer context") {
			t.Fatalf("error = %v, want the dist-boundary refusal", err)
		}
	})

	t.Run("a dist carrying a symlink is refused", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		dist := lgCovDist(t, "@acme/core", "1.1.0-dev")
		if err := os.Symlink(filepath.Join(dist, "package.json"), filepath.Join(dist, "alias.json")); err != nil {
			t.Fatal(err)
		}
		_, err := node.Link(ctx, consumer, "@acme/core", dist)
		if err == nil || !strings.Contains(err.Error(), "unsupported symlink") {
			t.Fatalf("error = %v, want the symlink refusal", err)
		}
		// The failed link must leave no stage, marker or target behind.
		if fileExists(linkAppliedMarkerPath(consumer, "@acme/core")) {
			t.Error("a failed link left its applied marker behind")
		}
		if fileExists(filepath.Join(consumer, "node_modules", "@acme", "core")) {
			t.Error("a failed link left a node_modules entry behind")
		}
	})
}

// Unlink restores whichever shape a link displaced and refuses when the
// on-disk state is not one it can prove. Each branch is asserted through the
// filesystem it leaves behind.
func TestLgCovUnlinkBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	node := ExecNode{}

	t.Run("an unreadable marker is reported", func(t *testing.T) {
		t.Parallel()
		consumer, _, marker, _ := lgCovStagedConsumer(t, "@acme/core")
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(marker, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := node.Unlink(ctx, consumer, "@acme/core")
		if err == nil || !strings.Contains(err.Error(), "applied-link marker") {
			t.Fatalf("error = %v, want the unreadable-marker report", err)
		}
	})

	t.Run("a recorded link that now dangles is reported", func(t *testing.T) {
		t.Parallel()
		consumer, target, _, _ := lgCovStagedConsumer(t, "@acme/core")
		missing := filepath.Join(consumer, "node_modules", ".pnpm", "gone", "node_modules", "@acme", "core")
		if err := os.Symlink(missing, target); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, target+linkSymlinkBackupSuffix, missing)
		_, err := node.Unlink(ctx, consumer, "@acme/core")
		if err == nil || !strings.Contains(err.Error(), "dangling") {
			t.Fatalf("error = %v, want the dangling-link report", err)
		}
	})

	t.Run("a superseded link whose record cannot be cleared is reported", func(t *testing.T) {
		t.Parallel()
		consumer, target, _, _ := lgCovStagedConsumer(t, "@acme/core")
		published := filepath.Join(consumer, "node_modules", ".pnpm", "@acme+core@2.0.0", "node_modules", "@acme", "core")
		lgCovWriteFile(t, filepath.Join(published, "package.json"), `{"name":"@acme/core","version":"2.0.0"}`)
		if err := os.Symlink(published, target); err != nil {
			t.Fatal(err)
		}
		// A stale directory backup that cannot be removed keeps the record.
		backup := target + linkBackupSuffix
		if err := os.MkdirAll(backup, 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, filepath.Join(backup, "keep.txt"), "keep\n")
		_, err := node.Unlink(ctx, consumer, "@acme/core")
		if err == nil {
			t.Fatal("Unlink reported success while the superseded record could not be cleared")
		}
		if got, readErr := os.Readlink(target); readErr != nil || got != published {
			t.Fatalf("the published link changed to %q (err %v)", got, readErr)
		}
	})

	t.Run("a record whose symlink vanished without backups is cleared", func(t *testing.T) {
		t.Parallel()
		consumer, target, marker, stage := lgCovStagedConsumer(t, "@acme/core")
		unknown := filepath.Join(consumer, "somewhere-else", "core")
		if err := os.MkdirAll(unknown, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(unknown, target); err != nil {
			t.Fatal(err)
		}
		note, err := node.Unlink(ctx, consumer, "@acme/core")
		if err != nil || note != "" {
			t.Fatalf("note = %q, err = %v, want the record cleared with no note", note, err)
		}
		if fileExists(marker) || fileExists(stage) {
			t.Error("closing an unresolvable record left its stage or marker behind")
		}
	})

	t.Run("a published directory supersedes the staged link", func(t *testing.T) {
		t.Parallel()
		consumer, target, marker, stage := lgCovStagedConsumer(t, "@acme/core")
		lgCovWriteFile(t, filepath.Join(target, "package.json"), `{"name":"@acme/core","version":"2.0.0"}`)
		note, err := node.Unlink(ctx, consumer, "@acme/core")
		if err != nil {
			t.Fatalf("Unlink: %v", err)
		}
		if note != "link superseded by published @acme/core@2.0.0; record cleared" {
			t.Fatalf("note = %q, want the supersession note", note)
		}
		if fileExists(marker) || fileExists(stage) {
			t.Error("clearing a superseded record left its stage or marker behind")
		}
		if published, err := os.ReadFile(filepath.Join(target, "package.json")); err != nil || !strings.Contains(string(published), "2.0.0") {
			t.Fatalf("the published directory was touched: %s (err %v)", published, err)
		}
	})

	t.Run("a replaced symlink with backups keeps refusing", func(t *testing.T) {
		t.Parallel()
		consumer, target, marker, stage := lgCovStagedConsumer(t, "@acme/core")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, target+linkSymlinkBackupSuffix, "wherever\n")
		_, err := node.Unlink(ctx, consumer, "@acme/core")
		if err == nil || !strings.Contains(err.Error(), "no longer the WB-created symlink") {
			t.Fatalf("error = %v, want the preserve-and-inspect refusal", err)
		}
		if !fileExists(marker) || !fileExists(stage) {
			t.Error("a refusal consumed the stage or marker")
		}
	})

	t.Run("an empty recorded link target is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		target := filepath.Join(consumer, "node_modules", "@acme", "core")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, target+linkSymlinkBackupSuffix, "")
		_, err := node.Unlink(ctx, consumer, "@acme/core")
		if err == nil || !strings.Contains(err.Error(), "recorded link target for @acme/core is empty") {
			t.Fatalf("error = %v, want the empty-record report", err)
		}
	})

	t.Run("restoring over an existing directory is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		target := filepath.Join(consumer, "node_modules", "@acme", "core")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, target+linkSymlinkBackupSuffix, "../../elsewhere/core")
		_, err := node.Unlink(ctx, consumer, "@acme/core")
		if err == nil || !strings.Contains(err.Error(), "restore the original link for @acme/core") {
			t.Fatalf("error = %v, want the restore failure", err)
		}
	})

	t.Run("an unreadable link record is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		target := filepath.Join(consumer, "node_modules", "@acme", "core")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(target+linkSymlinkBackupSuffix, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := node.Unlink(ctx, consumer, "@acme/core")
		if err == nil || !strings.Contains(err.Error(), "read the link record for @acme/core") {
			t.Fatalf("error = %v, want the unreadable-record report", err)
		}
	})

	t.Run("a directory backup that cannot be restored is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		target := filepath.Join(consumer, "node_modules", "@acme", "core")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, filepath.Join(target, "installed.txt"), "keep\n")
		backup := target + linkBackupSuffix
		if err := os.MkdirAll(backup, 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, filepath.Join(backup, "package.json"), `{"name":"@acme/core","version":"1.0.0"}`)
		_, err := node.Unlink(ctx, consumer, "@acme/core")
		if err == nil || !strings.Contains(err.Error(), "restore the installed @acme/core") {
			t.Fatalf("error = %v, want the restore-installed failure", err)
		}
		if !fileExists(filepath.Join(target, "installed.txt")) {
			t.Error("a failed restore destroyed what was already installed")
		}
	})
}

func TestLgCovResolvePublishedPackageRejections(t *testing.T) {
	t.Parallel()
	t.Run("a WB stage is never a published copy", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), ".core"+wbStageSuffix)
		lgCovWriteFile(t, filepath.Join(dir, "package.json"), `{"name":"@acme/core","version":"1.0.0"}`)
		if name, version, ok := resolvePublishedPackage(dir, "@acme/core"); ok || name != "" || version != "" {
			t.Fatalf("resolved %q@%q (ok=%v) from a WB stage", name, version, ok)
		}
	})

	t.Run("no manifest means no published copy", func(t *testing.T) {
		t.Parallel()
		if _, _, ok := resolvePublishedPackage(t.TempDir(), "@acme/core"); ok {
			t.Fatal("a directory without a manifest resolved as a published copy")
		}
	})

	t.Run("a malformed manifest means no published copy", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		lgCovWriteFile(t, filepath.Join(dir, "package.json"), `{"version":`)
		if _, _, ok := resolvePublishedPackage(dir, "@acme/core"); ok {
			t.Fatal("a malformed manifest resolved as a published copy")
		}
	})

	t.Run("a versionless manifest means no published copy", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		lgCovWriteFile(t, filepath.Join(dir, "package.json"), `{"name":"@acme/core"}`)
		if _, _, ok := resolvePublishedPackage(dir, "@acme/core"); ok {
			t.Fatal("a versionless manifest resolved as a published copy")
		}
	})

	t.Run("a nameless manifest falls back to the linked name", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		lgCovWriteFile(t, filepath.Join(dir, "package.json"), `{"version":"3.1.4"}`)
		name, version, ok := resolvePublishedPackage(dir, "@acme/core")
		if !ok || name != "@acme/core" || version != "3.1.4" {
			t.Fatalf("resolved %q@%q (ok=%v), want the linked name", name, version, ok)
		}
	})
}

// lgCovStagedPackage writes a package into the consumer's node_modules exactly
// the shape Link leaves: a real stage directory plus the applied-link marker
// naming it.
func lgCovStagedPackage(t *testing.T, consumer, packageName, manifest string) string {
	t.Helper()
	target := filepath.Join(consumer, "node_modules", filepath.FromSlash(packageName))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+wbStageSuffix)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if manifest != "" {
		lgCovWriteFile(t, filepath.Join(stage, "package.json"), manifest)
	}
	lgCovWriteFile(t, linkAppliedMarkerPath(consumer, packageName), stage+"\n")
	return stage
}

func TestLgCovLinkSiblingsFailurePaths(t *testing.T) {
	ctx := context.Background()
	node := ExecNode{}

	t.Run("a duplicate package name is reconciled once", func(t *testing.T) {
		consumer := t.TempDir()
		lgCovStagedPackage(t, consumer, "@acme/core", `{"name":"@acme/core"}`)
		bin := lgCovFakeBin(t, "node", `printf '{"visited":1,"mismatches":[],"error":""}'`)
		lgCovPrependPath(t, bin)
		if err := node.LinkSiblings(ctx, consumer, []string{"@acme/core", "@acme/core"}); err != nil {
			t.Fatalf("LinkSiblings with a duplicate name: %v", err)
		}
	})

	t.Run("a missing marker is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		err := node.LinkSiblings(ctx, consumer, []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "read staged sibling marker for @acme/core") {
			t.Fatalf("error = %v, want the missing-marker report", err)
		}
	})

	t.Run("an invalid marker is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		target := filepath.Join(consumer, "node_modules", "@acme", "core")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, linkAppliedMarkerPath(consumer, "@acme/core"), filepath.Join(consumer, "node_modules", ".wrong-name")+"\n")
		err := node.LinkSiblings(ctx, consumer, []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "validate staged sibling @acme/core") {
			t.Fatalf("error = %v, want the invalid-marker report", err)
		}
	})

	t.Run("an unreadable stage manifest is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovStagedPackage(t, consumer, "@acme/core", "")
		err := node.LinkSiblings(ctx, consumer, []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "read staged package manifest for @acme/core") {
			t.Fatalf("error = %v, want the unreadable-manifest report", err)
		}
	})

	t.Run("a malformed stage manifest is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovStagedPackage(t, consumer, "@acme/core", `{"dependencies":`)
		err := node.LinkSiblings(ctx, consumer, []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "parse staged package manifest for @acme/core") {
			t.Fatalf("error = %v, want the malformed-manifest report", err)
		}
	})

	t.Run("a staged sibling pointing elsewhere is refused", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		appStage := lgCovStagedPackage(t, consumer, "@acme/app", `{"name":"@acme/app","dependencies":{"@acme/core":"1.0.0"}}`)
		coreStage := lgCovStagedPackage(t, consumer, "@acme/core", `{"name":"@acme/core"}`)
		elsewhere := t.TempDir()
		linkParent := filepath.Join(appStage, "node_modules", "@acme")
		if err := os.MkdirAll(linkParent, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(linkParent, "core")); err != nil {
			t.Fatal(err)
		}
		err := node.LinkSiblings(ctx, consumer, []string{"@acme/app", "@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "points to") || !strings.Contains(err.Error(), coreStage) {
			t.Fatalf("error = %v, want the wrong-target refusal naming %s", err, coreStage)
		}
	})

	t.Run("an unstattable staged sibling path is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		appStage := lgCovStagedPackage(t, consumer, "@acme/app", `{"name":"@acme/app","dependencies":{"@acme/core":"1.0.0"}}`)
		lgCovStagedPackage(t, consumer, "@acme/core", `{"name":"@acme/core"}`)
		// `node_modules/@acme` as a regular file makes Lstat of the edge fail
		// with ENOTDIR — neither "absent" nor a shape WB may replace.
		lgCovWriteFile(t, filepath.Join(appStage, "node_modules", "@acme"), "not a directory\n")
		err := node.LinkSiblings(ctx, consumer, []string{"@acme/app", "@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "inspect staged sibling path") {
			t.Fatalf("error = %v, want the unstattable-path report", err)
		}
	})

	t.Run("existing correct edges are left in place on a retry", func(t *testing.T) {
		consumer := t.TempDir()
		appStage := lgCovStagedPackage(t, consumer, "@acme/app", `{"name":"@acme/app","dependencies":{"@acme/core":"1.0.0"}}`)
		coreStage := lgCovStagedPackage(t, consumer, "@acme/core", `{"name":"@acme/core"}`)
		bin := lgCovFakeBin(t, "node", `printf '{"visited":1,"mismatches":[],"error":""}'`)
		lgCovPrependPath(t, bin)

		if err := node.LinkSiblings(ctx, consumer, []string{"@acme/app", "@acme/core"}); err != nil {
			t.Fatalf("first reconciliation: %v", err)
		}
		edge := filepath.Join(appStage, "node_modules", "@acme", "core")
		first, err := os.Readlink(edge)
		if err != nil {
			t.Fatalf("the sibling edge was not created: %v", err)
		}
		if !samePath(filepath.Dir(edge), first, coreStage) {
			t.Fatalf("edge points at %q, want %s", first, coreStage)
		}
		// The retry must accept the edge it already created — preflight passes
		// and the create loop skips it rather than failing on EEXIST.
		if err := node.LinkSiblings(ctx, consumer, []string{"@acme/app", "@acme/core"}); err != nil {
			t.Fatalf("second reconciliation: %v", err)
		}
		second, err := os.Readlink(edge)
		if err != nil || second != first {
			t.Fatalf("retry rewrote the edge to %q (err %v), want %q", second, err, first)
		}
	})
}

func TestLgCovSamePathAndRemoveSiblingEdges(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	if !samePath(base, "relative/target", filepath.Join(base, "relative", "target")) {
		t.Fatal("samePath rejected a relative target that names the wanted absolute path")
	}
	if samePath(base, "relative/other", filepath.Join(base, "relative", "target")) {
		t.Fatal("samePath accepted a relative target naming a different path")
	}

	root := t.TempDir()
	first := filepath.Join(root, "one")
	second := filepath.Join(root, "two")
	lgCovWriteFile(t, first, "x\n")
	lgCovWriteFile(t, second, "x\n")
	removeSiblingEdges([]string{first, second})
	for _, path := range []string{first, second} {
		if fileExists(path) {
			t.Fatalf("%s survived removeSiblingEdges", path)
		}
	}
	// A path that is already gone must not panic or fail the whole cleanup.
	removeSiblingEdges([]string{first})
}

// verifyRuntimeGraph's failure surface, driven entirely through a fake node so
// every branch is deterministic.
func TestLgCovVerifyRuntimeGraphFailurePaths(t *testing.T) {
	ctx := context.Background()

	t.Run("node absent", func(t *testing.T) {
		lgCovRestrictPath(t, t.TempDir())
		node := ExecNode{Timeout: 30 * time.Second}
		err := node.verifyRuntimeGraph(ctx, t.TempDir(), []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "node is required to verify") {
			t.Fatalf("error = %v, want the missing-node refusal", err)
		}
	})

	t.Run("node times out", func(t *testing.T) {
		bin := lgCovFakeBin(t, "node", "sleep 5")
		lgCovPrependPath(t, bin)
		node := ExecNode{Timeout: 100 * time.Millisecond}
		err := node.verifyRuntimeGraph(ctx, t.TempDir(), []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "timed out after") {
			t.Fatalf("error = %v, want the timeout report", err)
		}
	})

	t.Run("node fails", func(t *testing.T) {
		bin := lgCovFakeBin(t, "node", "printf 'node exploded\\n' >&2; exit 3")
		lgCovPrependPath(t, bin)
		node := ExecNode{Timeout: 30 * time.Second}
		err := node.verifyRuntimeGraph(ctx, t.TempDir(), []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "verify linked npm runtime graph with node") || !strings.Contains(err.Error(), "node exploded") {
			t.Fatalf("error = %v, want the node failure reported", err)
		}
	})

	t.Run("node output is not JSON", func(t *testing.T) {
		bin := lgCovFakeBin(t, "node", "printf 'not json at all'")
		lgCovPrependPath(t, bin)
		node := ExecNode{Timeout: 30 * time.Second}
		err := node.verifyRuntimeGraph(ctx, t.TempDir(), []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "parse linked npm runtime graph result") {
			t.Fatalf("error = %v, want the parse failure", err)
		}
	})

	t.Run("the probe reports an error", func(t *testing.T) {
		bin := lgCovFakeBin(t, "node", `printf '{"visited":0,"mismatches":[],"error":"consumer root cannot resolve linked package"}'`)
		lgCovPrependPath(t, bin)
		node := ExecNode{Timeout: 30 * time.Second}
		err := node.verifyRuntimeGraph(ctx, t.TempDir(), []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "consumer root cannot resolve linked package") {
			t.Fatalf("error = %v, want the probe error surfaced", err)
		}
	})

	t.Run("more than eight split edges are summarised", func(t *testing.T) {
		mismatch := `{"from":"consumer root","dependency":"@acme/core","resolver":"createRequire.resolve","resolved_root":"a","expected_root":"b"}`
		entries := make([]string, 0, 9)
		for index := 0; index < 9; index++ {
			entries = append(entries, strings.Replace(mismatch, `"@acme/core"`, `"@acme/p`+string(rune('a'+index))+`"`, 1))
		}
		payload := `{"visited":12,"mismatches":[` + strings.Join(entries, ",") + `],"error":""}`
		bin := lgCovFakeBin(t, "node", "printf '%s' '"+payload+"'")
		lgCovPrependPath(t, bin)
		node := ExecNode{Timeout: 30 * time.Second}
		err := node.verifyRuntimeGraph(ctx, t.TempDir(), []string{"@acme/core"})
		if err == nil || !strings.Contains(err.Error(), "and 1 more split edges") || !strings.Contains(err.Error(), "after checking 12 installed packages") {
			t.Fatalf("error = %v, want the capped report with the remaining count", err)
		}
	})
}

// A clean graph resolves with the default timeout when ExecNode.Timeout is
// unset, which is the production shape of the port.
func TestLgCovVerifyRuntimeGraphAcceptsACleanGraph(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	consumer := t.TempDir()
	lgCovWriteFile(t, filepath.Join(consumer, "package.json"), `{"name":"consumer","dependencies":{"@acme/core":"1.0.0"}}`)
	lgCovWriteFile(t, filepath.Join(consumer, "node_modules", "@acme", "core", "package.json"), `{"name":"@acme/core","version":"1.0.0","main":"index.js"}`)
	lgCovWriteFile(t, filepath.Join(consumer, "node_modules", "@acme", "core", "index.js"), "module.exports = {};\n")

	node := ExecNode{}
	if err := node.verifyRuntimeGraph(context.Background(), consumer, []string{"@acme/core", "@acme/core"}); err != nil {
		t.Fatalf("a clean runtime graph was rejected: %v", err)
	}
}
