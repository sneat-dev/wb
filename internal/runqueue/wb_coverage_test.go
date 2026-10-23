package runqueue

import "testing"

func TestClassifyWBCoverageUsesTheActualRootCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		argv []string
		want Kind
	}{
		{"coverage", []string{"wb", "coverage", "."}, KindRaceOrCover},
		{"installed path", []string{"/opt/homebrew/bin/wb", "coverage", ".", "--include-e2e", "--timeout", "35m"}, KindRaceOrCover},
		{"Windows executable suffix", []string{"wb.exe", "coverage"}, KindRaceOrCover},
		{"uppercase executable suffix", []string{"WB.EXE", "coverage"}, KindRaceOrCover},
		{"leading globals", []string{"wb", "--projects-root", "/private/tmp/projects", "--filter=coverage", "--org", "acme", "--org=other", "--non-interactive", "--quiet=false", "coverage", "."}, KindRaceOrCover},
		{"root positional terminator", []string{"wb", "--", "coverage", "."}, KindRaceOrCover},
		{"help false", []string{"wb", "coverage", "--help=false"}, KindRaceOrCover},
		{"short help false", []string{"wb", "coverage", "-h=false"}, KindRaceOrCover},
		{"last help false", []string{"wb", "--help", "coverage", "--help=false"}, KindRaceOrCover},
		{"help after positional terminator", []string{"wb", "coverage", "--", "--help"}, KindRaceOrCover},
		{"help after positional argument is conservative", []string{"wb", "coverage", ".", "--help"}, KindRaceOrCover},
		{"flag value is not help", []string{"wb", "coverage", "--report-dir", "--help"}, KindRaceOrCover},
		{"baseline artifact", []string{"wb", "coverage", "baseline", "profile.cov"}, KindNone},
		{"summary artifact", []string{"wb", "coverage", "summary", "profile.cov"}, KindNone},
		{"worklist artifact", []string{"wb", "coverage", "worklist", "profile.cov"}, KindNone},

		{"no arguments", []string{"wb"}, KindNone},
		{"globals without verb", []string{"wb", "--quiet", "--projects-root=coverage"}, KindNone},
		{"help command", []string{"wb", "help", "coverage"}, KindNone},
		{"root help", []string{"wb", "--help", "coverage"}, KindNone},
		{"verb help", []string{"wb", "coverage", "--help"}, KindNone},
		{"short help", []string{"wb", "coverage", "-h"}, KindNone},
		{"explicit help", []string{"wb", "coverage", "--help=true"}, KindNone},
		{"version", []string{"wb", "--version", "coverage"}, KindNone},
		{"unrelated verb", []string{"wb", "worktree", "inspect", "coverage"}, KindNone},
		{"global value is not verb", []string{"wb", "--projects-root", "coverage", "worktree", "list"}, KindNone},
		{"another global value is not verb", []string{"wb", "--filter", "coverage", "worktree", "list"}, KindNone},
		{"missing global value", []string{"wb", "--projects-root"}, KindNone},
		{"unknown leading flag", []string{"wb", "--unknown", "coverage"}, KindNone},
		{"other executable", []string{"not-wb", "coverage"}, KindNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Classify(tc.argv); got != tc.want {
				t.Fatalf("Classify(%q)=%v want %v", tc.argv, got, tc.want)
			}
		})
	}
}
