package orchestrate

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveExtractionReportsOwnedFilesystemRefusals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"directory parent", "regular parent", "symlink parent", "symlink destination"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			destination := t.TempDir()
			blocker := filepath.Join(destination, "blocked")
			header := &tar.Header{Name: "blocked/child", Typeflag: tar.TypeDir, Mode: 0o750}
			switch mode {
			case "regular parent":
				header.Name = "blocked/file"
				header.Typeflag = tar.TypeReg
				header.Size = 1
			case "symlink parent":
				header.Name = "blocked/link"
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "file"
			case "symlink destination":
				header.Name = "link"
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "file"
				blocker = filepath.Join(destination, "link")
			}
			if err := os.WriteFile(blocker, []byte("owned blocker\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			archive := orchCovTar(t, func(w *tar.Writer) {
				if err := w.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
				if header.Typeflag == tar.TypeReg {
					if _, err := w.Write([]byte("x")); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.WriteHeader(&tar.Header{Name: "later", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}); err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write([]byte("y")); err != nil {
					t.Fatal(err)
				}
			})
			err := extractWorktreeMergeArchive(orchCovWriteArchive(t, archive), destination)
			if mode == "symlink destination" {
				var linkErr *os.LinkError
				if !errors.As(err, &linkErr) || linkErr.Op != "symlink" || linkErr.New != blocker || linkErr.Old != "file" {
					t.Fatalf("actual symlink creation refusal = %v", err)
				}
			} else {
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) || pathErr.Op != "mkdir" || (pathErr.Path != blocker && !strings.HasPrefix(pathErr.Path, blocker+string(filepath.Separator))) {
					t.Fatalf("actual %s mkdir refusal = %v", mode, err)
				}
			}
			if body, e := os.ReadFile(blocker); e != nil || string(body) != "owned blocker\n" {
				t.Fatalf("refused extraction changed blocker: %q,%v", body, e)
			}
			if _, e := os.Stat(filepath.Join(destination, "later")); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("extraction continued after %s refusal: %v", mode, e)
			}
		})
	}
}
