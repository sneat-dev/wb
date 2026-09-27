package main

import (
	"strings"
	"testing"
	"time"
)

func TestWorktreeMergeDirectCIDeferralFlagRequiresExplicitDirectRoute(t *testing.T) {
	base := worktreeMergeFlags{route: "direct", directCIPullRequest: "773", timeout: time.Second, format: "text"}
	if err := validateWorktreeMergeFlags(base); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		apply func(*worktreeMergeFlags)
	}{
		{"auto route", func(flags *worktreeMergeFlags) { flags.route = "auto" }},
		{"PR route", func(flags *worktreeMergeFlags) { flags.route = "pr" }},
		{"local validation", func(flags *worktreeMergeFlags) { flags.validateLocally = true }},
		{"unfenced", func(flags *worktreeMergeFlags) { flags.allowUnfenced = true }},
	} {
		t.Run(change.name, func(t *testing.T) {
			flags := base
			change.apply(&flags)
			if err := validateWorktreeMergeFlags(flags); err == nil || !strings.Contains(err.Error(), "--defer-direct-ci-pr") {
				t.Fatalf("invalid direct CI flags accepted: %+v, err=%v", flags, err)
			}
		})
	}
}
