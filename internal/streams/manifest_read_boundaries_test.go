package streams

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestDiscoveryReportsChangesBetweenScanAndRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, manifest string
		at             int
		corrupt        bool
	}{
		{"published Go", "go.mod", 1, false},
		{"published npm", "libs/core/package.json", 2, false},
		{"declared Go", "go.mod", 2, false},
		{"declared npm missing", "package.json", 2, false},
		{"declared npm malformed", "package.json", 2, true},
		{"provider npm", "package.json", 2, false},
		{"scanner npm", "package.json", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := writeTree(t, map[string]string{tc.manifest: `{"name":"@acme/core"}`})
			if strings.HasSuffix(tc.manifest, "go.mod") {
				if err := os.WriteFile(filepath.Join(root, tc.manifest), []byte("module acme/core\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			read := func(path string) ([]byte, error) {
				if path == filepath.Join(root, tc.manifest) {
					reads++
					if reads == tc.at {
						var err error
						if tc.corrupt {
							err = os.WriteFile(path, []byte("{"), 0600)
						} else {
							err = os.Remove(path)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				return os.ReadFile(path)
			}
			var err error
			switch {
			case strings.HasPrefix(tc.name, "published"):
				_, err = discoverPublishedWithRead(root, read)
			case strings.HasPrefix(tc.name, "declared"):
				_, err = discoverDeclarationsWithRead(root, nil, read)
			case strings.HasPrefix(tc.name, "provider"):
				names, finding := collectNpmPackageNamesWithRead(PreflightInput{Repository: "acme/core", Path: root}, read)
				if names != nil || finding.Status != PreflightUnknown || !strings.Contains(finding.Detail, "no such file") {
					t.Fatalf("names=%v finding=%+v", names, finding)
				}
				return
			default:
				_, err = npmPackageManifestsWithRead(root, read)
			}
			if err == nil || !strings.Contains(err.Error(), tc.manifest) {
				t.Fatalf("read %d: error=%v", reads, err)
			}
			if !tc.corrupt && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("error=%v, want native missing-file failure", err)
			}
		})
	}
}
