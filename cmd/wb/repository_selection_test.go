package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

func TestRepositorySelectionAdapterPreservesLegacyFieldsAndAdmission(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, repo := range []string{"acme/core", "acme/other"} {
		if err := os.MkdirAll(filepath.Join(root, repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := qualityTargets("", root, "acme", qualityOptions{fleet: true, parallel: 2, match: "acme/*", regex: "core$"})
	if err != nil {
		t.Fatal(err)
	}
	want := []qualityTarget{{repository: "acme/core", path: filepath.Join(root, "acme/core")}}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets=%#v", targets)
	}
	if _, err := qualityTargets("", root, "", qualityOptions{parallel: 1, retry: -1}); err == nil || err.Error() != "retry count must not be negative" {
		t.Fatalf("retry=%v", err)
	}
	if _, err := qualityTargets("", root, "", qualityOptions{parallel: 1, timeout: -1}); err == nil || err.Error() != "timeout must not be negative" {
		t.Fatalf("timeout=%v", err)
	}
	empty, err := qualityTargets("", t.TempDir(), "", qualityOptions{fleet: true, parallel: 1, allowEmpty: true})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty=%v error=%v", empty, err)
	}
}

func TestRepositorySelectionAdaptersDelegateMatchingAndDispatch(t *testing.T) {
	t.Parallel()
	if !matchesQualityTarget("acme/core", "acme", "acme/*", regexp.MustCompile("core$")) {
		t.Fatal("matching adapter lost selectors")
	}
	slots := make([]int, 5)
	runTargets(len(slots), 2, func(index int) { slots[index] = index + 1 })
	if !reflect.DeepEqual(slots, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("slots=%v", slots)
	}
}
