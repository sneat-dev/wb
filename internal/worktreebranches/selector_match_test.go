package worktreebranches

import "testing"

func TestDependencySelectorRequiresExactPackageIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, selector, packageName string
		want                        bool
	}{
		{"direct package", "@sneat/core", "@sneat/core", true},
		{"empty identity", "", "", false},
		{"package prefix", "@sneat/core-extra", "@sneat/core", false},
		{"npm lock entry", "packages|node_modules/@sneat/core|version", "@sneat/core", true},
		{"pnpm lock entry", "snapshots|/@sneat/core@1.2.3|version", "@sneat/core", true},
		{"lock entry prefix", "snapshots|/@sneat/core-extra@1.2.3|version", "@sneat/core", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SelectorNamesExactPackage(tc.selector, tc.packageName); got != tc.want {
				t.Fatalf("selector %q for package %q: got %v, want %v", tc.selector, tc.packageName, got, tc.want)
			}
		})
	}
}
