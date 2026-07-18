package recipe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppliesToAlways(t *testing.T) {
	r := Recipe{AppliesIf: "always"}
	ok, err := r.AppliesTo(t.TempDir())
	if err != nil || !ok {
		t.Errorf("ok=%v err=%v, want true, nil", ok, err)
	}
}

func TestAppliesToHasFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "specscore.yaml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Recipe{AppliesIf: "has_file:specscore.yaml"}
	ok, err := r.AppliesTo(dir)
	if err != nil || !ok {
		t.Errorf("ok=%v err=%v, want true, nil", ok, err)
	}

	r2 := Recipe{AppliesIf: "has_file:missing.yaml"}
	ok2, err2 := r2.AppliesTo(dir)
	if err2 != nil || ok2 {
		t.Errorf("ok=%v err=%v, want false, nil", ok2, err2)
	}
}

func TestAppliesToHasSourceSingle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Recipe{AppliesIf: "has_source:go"}
	ok, err := r.AppliesTo(dir)
	if err != nil || !ok {
		t.Errorf("ok=%v err=%v, want true, nil", ok, err)
	}

	r2 := Recipe{AppliesIf: "has_source:ts"}
	ok2, err2 := r2.AppliesTo(dir)
	if err2 != nil || ok2 {
		t.Errorf("ok=%v err=%v, want false, nil (only .go present)", ok2, err2)
	}
}

func TestAppliesToHasSourceCommaListIsOr(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Recipe{AppliesIf: "has_source:go,ts"}
	ok, err := r.AppliesTo(dir)
	if err != nil || !ok {
		t.Errorf("ok=%v err=%v, want true, nil (ts present, go,ts is OR)", ok, err)
	}
}

func TestAppliesToUnknownVocabulary(t *testing.T) {
	r := Recipe{AppliesIf: "something_else"}
	if _, err := r.AppliesTo(t.TempDir()); err == nil {
		t.Error("expected error for unknown applies_if vocabulary, got nil")
	}
}

func TestAppliesToUnknownLanguage(t *testing.T) {
	r := Recipe{AppliesIf: "has_source:rust"}
	if _, err := r.AppliesTo(t.TempDir()); err == nil {
		t.Error("expected error for unknown has_source language, got nil")
	}
}
