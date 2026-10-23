package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateWorktreeAtPlacementRefusesEachPublicationBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want string
	}{
		{"open canonical", "open unavailable"},
		{"resolve policy", "policy unavailable"},
		{"policy mismatch", "does not match"},
		{"resolve path", "path unavailable"},
		{"split repository", "must be owner/name"},
		{"branch lookup", "branch unavailable"},
		{"branch occupancy lookup", "occupancy unavailable"},
		{"branch occupied", "already checked out"},
		{"local root", "local unavailable"},
		{"local root changed", "local worktree root changed"},
		{"operation root", "operation unavailable"},
		{"destination", "destination unavailable"},
		{"destination mismatch", "does not match"},
		{"destination exists", "already exists"},
		{"publish", "publish unavailable"},
		{"missing publication receipt", "no publication receipt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			placement := WorktreePlacement{Root: root, relative: "acme/app"}
			canonical := &canonicalRepository{path: filepath.Join(root, "canonical")}
			configured := worktreePlacement{Root: root, Relative: "acme/app"}
			branchExists := test.name == "branch occupancy lookup" || test.name == "branch occupied"
			if strings.HasPrefix(test.name, "local root") {
				placement.RepositoryLocal, configured.Local = true, true
			}
			ports := placementCreatePorts{
				openCanonical: func(string) (*canonicalRepository, error) {
					if test.name == "open canonical" {
						return nil, errors.New("open unavailable")
					}
					return canonical, nil
				},
				configured: func(context.Context, string, *canonicalRepository, string) (worktreePlacement, error) {
					if test.name == "resolve policy" {
						return worktreePlacement{}, errors.New("policy unavailable")
					}
					if test.name == "policy mismatch" {
						return worktreePlacement{Root: filepath.Join(root, "other")}, nil
					}
					return configured, nil
				},
				path: func(_ WorktreePlacement, _, _ string) (string, error) {
					if test.name == "resolve path" {
						return "", errors.New("path unavailable")
					}
					if test.name == "split repository" {
						return filepath.Join(root, "task", "acme", "app"), nil
					}
					if placement.RepositoryLocal {
						return filepath.Join(root, "task"), nil
					}
					return filepath.Join(root, "task", "acme", "app"), nil
				},
				branchExists: func(context.Context, *canonicalRepository, string) (bool, error) {
					if test.name == "branch lookup" {
						return false, errors.New("branch unavailable")
					}
					return branchExists, nil
				},
				branchWorktree: func(context.Context, *canonicalRepository, string) (bool, string, error) {
					if test.name == "branch occupancy lookup" {
						return false, "", errors.New("occupancy unavailable")
					}
					return true, "/occupied", nil
				},
				prepareLocal: func(context.Context, *canonicalRepository, string) (string, *os.File, error) {
					if test.name == "local root" {
						return "", nil, errors.New("local unavailable")
					}
					if test.name == "local root changed" {
						return filepath.Join(root, "other"), &os.File{}, nil
					}
					return root, &os.File{}, nil
				},
				prepareOperation: func(_, _ string) (preparedOperationRoot, error) {
					if test.name == "operation root" {
						return preparedOperationRoot{}, errors.New("operation unavailable")
					}
					return preparedOperationRoot{Path: filepath.Join(root, "task")}, nil
				},
				prepareDestination: func(operationRoot string, _ *os.File, parent, repository string) (string, bool, error) {
					if test.name == "destination" {
						return "", false, errors.New("destination unavailable")
					}
					if test.name == "destination mismatch" {
						return filepath.Join(root, "wrong"), false, nil
					}
					return filepath.Join(operationRoot, parent, repository), test.name == "destination exists", nil
				},
				publish: func(_ context.Context, request securePublicationRequest) error {
					if test.name == "publish" {
						return errors.New("publish unavailable")
					}
					if test.name != "missing publication receipt" {
						*request.publication = &createdWorktreePublication{}
					}
					return nil
				},
			}
			repository := "acme/app"
			if test.name == "split repository" {
				repository = "bad"
			}
			_, err := createWorktreeAtPlacementWith(context.Background(), root, canonical.path, placement,
				"task", repository, "feature", "main", strings.Repeat("a", 40), ports)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s refusal = %v, want %q", test.name, err, test.want)
			}
		})
	}
}
