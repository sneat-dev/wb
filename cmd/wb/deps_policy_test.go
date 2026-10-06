package main

import (
	"errors"
	"testing"
)

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return exitOK
	}
	var coded *exitError
	if errors.As(err, &coded) {
		return coded.code
	}
	t.Fatalf("expected an exitError, got %T: %v", err, err)
	return -1
}
