package main

import (
	"strings"
	"testing"
)

func TestStatusMarkdownReportsAttention(t *testing.T) {
	report := statusIndex{Repositories: []repositoryStatusInfo{{Repository: "acme/repo", Status: "attention", Summary: "1 modified file", Modified: []string{"main.go"}}}}
	markdown := statusMarkdown(report, true, "# WB local repository status\n\n")
	for _, want := range []string{"attention", "1 modified file", "main.go"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("status markdown missing %q:\n%s", want, markdown)
		}
	}
}
