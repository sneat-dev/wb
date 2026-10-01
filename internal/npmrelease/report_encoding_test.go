package npmrelease

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReportEncodingFailurePreservesArtifacts(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("cannot serialize report")
			for _, existing := range []bool{false, true} {
				directory := t.TempDir()
				old := []byte("previous complete generation\n")
				names := []string{"npm-publish.json", "npm-publish.yaml"}
				if existing {
					for _, name := range names {
						if err := os.WriteFile(filepath.Join(directory, name), old, 0o644); err != nil {
							t.Fatal(err)
						}
					}
				}
				yamlEncoder, jsonEncoder := Report.YAML, Report.JSON
				reject := func(Report) ([]byte, error) { return nil, failure }
				if format == "yaml" {
					yamlEncoder = reject
				} else {
					jsonEncoder = reject
				}
				if err := writeReportEncoded(directory, Report{}, yamlEncoder, jsonEncoder); !errors.Is(err, failure) {
					t.Fatalf("encoding error = %v", err)
				}
				for _, name := range names {
					content, err := os.ReadFile(filepath.Join(directory, name))
					if existing {
						if err != nil || string(content) != string(old) {
							t.Fatalf("previous %s changed: %q, %v", name, content, err)
						}
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("partial %s exists: %q, %v", name, content, err)
					}
				}
				entries, err := os.ReadDir(directory)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if existing {
					want = 2
				}
				if len(entries) != want {
					t.Fatalf("leftover artifacts = %v", entries)
				}
			}
		})
	}
}
