package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modulezip "golang.org/x/mod/zip"
)

// TestModuleArchiveIncludesCmdWBEmbedInputs protects `go install
// github.com/sneat-dev/wb/cmd/wb@<revision>`. It creates the archive from the
// committed VCS revision, matching what `go install ...@<revision>` consumes:
// mutable local WB state must not make the publication check flaky. Go module
// archives omit paths named vendor, so an embed input can compile locally yet
// be absent from the published module.
func TestModuleArchiveIncludesCmdWBEmbedInputs(t *testing.T) {
	t.Parallel()

	root := moduleArchiveTestRoot(t)
	requiredFiles := append(cmdWBEmbedFiles(t, root),
		"internal/secretscan/gitleaks/LICENSE",
		"internal/secretscan/gitleaks/PROVENANCE.md",
	)
	sort.Strings(requiredFiles)

	var archive bytes.Buffer
	moduleVersion := module.Version{Path: "github.com/sneat-dev/wb", Version: "v0.0.0"}
	createModuleArchiveFromVCS(t, &archive, moduleVersion, root)

	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatalf("read module archive: %v", err)
	}
	archived := make(map[string]struct{}, len(reader.File))
	for _, file := range reader.File {
		archived[file.Name] = struct{}{}
	}

	prefix := moduleVersion.Path + "@" + moduleVersion.Version + "/"
	var missing []string
	for _, file := range requiredFiles {
		if _, ok := archived[prefix+file]; !ok {
			missing = append(missing, file)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("module archive omits required cmd/wb file(s): %s", strings.Join(missing, ", "))
	}
}

func TestModuleArchiveUsesCommittedPublicationSnapshot(t *testing.T) {
	root := t.TempDir()
	writeModuleArchiveTestFile(t, root, "go.mod", "module example.com/archive-test\n\ngo 1.25\n")
	writeModuleArchiveTestFile(t, root, "assets/embedded.txt", "published\n")
	writeModuleArchiveTestFile(t, root, "vendor/example.com/archive/not-published.txt", "vendor\n")
	runModuleArchiveGit(t, root, "init")
	runModuleArchiveGit(t, root, "config", "user.email", "wb-test@example.invalid")
	runModuleArchiveGit(t, root, "config", "user.name", "WB archive test")
	runModuleArchiveGit(t, root, "add", ".")
	runModuleArchiveGit(t, root, "commit", "-m", "published snapshot")

	// A working tree may contain heartbeat state or edits while tests run. The
	// module archive must remain the exact committed source that a revisioned
	// install publishes, not a race-prone directory walk.
	writeModuleArchiveTestFile(t, root, "assets/embedded.txt", "working-tree-only\n")
	writeModuleArchiveTestFile(t, root, ".wb/local/heartbeat.tmp", "transient\n")

	moduleVersion := module.Version{Path: "example.com/archive-test", Version: "v0.0.0"}
	var archive bytes.Buffer
	createModuleArchiveFromVCS(t, &archive, moduleVersion, root)
	files := moduleArchiveContents(t, archive.Bytes())
	prefix := moduleVersion.Path + "@" + moduleVersion.Version + "/"
	if got := string(files[prefix+"assets/embedded.txt"]); got != "published\n" {
		t.Fatalf("archived embedded file = %q, want committed content", got)
	}
	for _, excluded := range []string{
		prefix + ".wb/local/heartbeat.tmp",
		prefix + "vendor/example.com/archive/not-published.txt",
	} {
		if _, ok := files[excluded]; ok {
			t.Fatalf("module archive unexpectedly includes %s", excluded)
		}
	}
}

func createModuleArchiveFromVCS(t *testing.T, archive io.Writer, moduleVersion module.Version, root string) {
	t.Helper()
	revision, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve committed module revision: %v", err)
	}
	// golang.org/x/mod's VCS recognizer intentionally expects .git to be a
	// directory. WB tests run in linked worktrees, where .git is a file, so use
	// an isolated local clone while still asking the modulezip library to build
	// the exact committed VCS snapshot.
	archiveRoot := filepath.Join(t.TempDir(), "published-source")
	runModuleArchiveGit(t, root, "clone", "--quiet", "--no-checkout", root, archiveRoot)
	runModuleArchiveGit(t, archiveRoot, "checkout", "--quiet", "--detach", strings.TrimSpace(string(revision)))
	if err := modulezip.CreateFromVCS(archive, moduleVersion, archiveRoot, strings.TrimSpace(string(revision)), ""); err != nil {
		t.Fatalf("create module archive from VCS: %v", err)
	}
}

func moduleArchiveContents(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("read module archive: %v", err)
	}
	contents := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatalf("open archived %s: %v", file.Name, err)
		}
		contents[file.Name], err = io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil {
			t.Fatalf("read archived %s: %v", file.Name, err)
		}
		if closeErr != nil {
			t.Fatalf("close archived %s: %v", file.Name, closeErr)
		}
	}
	return contents
}

func writeModuleArchiveTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func runModuleArchiveGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

type moduleArchivePackage struct {
	Dir        string
	EmbedFiles []string
}

func cmdWBEmbedFiles(t *testing.T, root string) []string {
	t.Helper()

	command := exec.Command("go", "list", "-deps", "-json", "./cmd/wb")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list cmd/wb dependencies: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	embedFiles := make(map[string]struct{})
	for {
		var pkg moduleArchivePackage
		err := decoder.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if len(pkg.EmbedFiles) == 0 {
			continue
		}

		dir, ok := moduleRelativePath(root, pkg.Dir)
		if !ok {
			continue
		}
		for _, embedded := range pkg.EmbedFiles {
			embedFiles[filepath.ToSlash(filepath.Join(dir, embedded))] = struct{}{}
		}
	}
	if len(embedFiles) == 0 {
		t.Fatal("go list found no go:embed input in cmd/wb dependencies")
	}

	files := make([]string, 0, len(embedFiles))
	for file := range embedFiles {
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}

func moduleRelativePath(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func moduleArchiveTestRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate module archive test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}
