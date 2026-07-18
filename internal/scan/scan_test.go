package scan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasExtFinds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := HasExt(dir, ".go")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected to find a .go file")
	}
}

func TestHasExtNoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := HasExt(dir, ".go", ".ts")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected no match for .go/.ts when only README.md exists")
	}
}

func TestHasExtSkipsVendorDirs(t *testing.T) {
	dir := t.TempDir()
	vendor := filepath.Join(dir, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendor, "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := HasExt(dir, ".go")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected vendor/ to be skipped")
	}
}
