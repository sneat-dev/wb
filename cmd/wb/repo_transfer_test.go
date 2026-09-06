package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRepoTransferCleanupRequiresReceiptAsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"repo", "transfer", "cleanup", "--non-interactive"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit code = %d, want %d (usage); stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--receipt is required") {
		t.Fatalf("stderr does not explain missing receipt: %s", stderr.String())
	}
}
