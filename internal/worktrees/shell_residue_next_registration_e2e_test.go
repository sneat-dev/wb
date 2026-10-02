//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EShellResidueNextRegistrationRetainsNativeAuthorityAndGitRefusals(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"closed_authority", "uninitialized_git"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			canonical := shellResidueNextPhysicalTemp(t)
			if err := os.Mkdir(filepath.Join(canonical, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			checkout := filepath.Join(t.TempDir(), "checkout")
			observed := false
			registered, err := worktreeStillRegisteredObserved(context.Background(), canonical, checkout, func(held *canonicalRepository) {
				observed = true
				if name == "closed_authority" {
					if err := held.root.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
			want := "list worktree registrations"
			if name == "closed_authority" {
				want = "canonical repository path changed"
			}
			if !observed || registered || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("registration refused=%v %v observed=%v", registered, err, observed)
			}
			if _, err := os.Stat(filepath.Join(canonical, ".git")); err != nil {
				t.Fatalf("refusal removed Git authority: %v", err)
			}
			if _, err := os.Stat(checkout); !os.IsNotExist(err) {
				t.Fatalf("refusal created checkout: %v", err)
			}
		})
	}
}
