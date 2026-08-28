package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
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
// github.com/sneat-dev/wb/cmd/wb@<revision>`. Go module archives omit paths
// named vendor, so an embed input can compile locally yet be absent from the
// published module.
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
	if err := modulezip.CreateFromDir(&archive, moduleVersion, root); err != nil {
		t.Fatalf("create module archive: %v", err)
	}

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
