//go:build windows

package herdr

import (
	"context"
	"testing"
)

func TestOriginalHerdrPipeMappingPreservesRecordedNameExactly(t *testing.T) {
	t.Parallel()
	for _, recorded := range []string{`C:\Users\alex\herdr.sock`, `C:/Users/é/终端.sock`, `\\.\pipe\already-prefixed`, `mixed/path\part`} {
		mapped, err := herdrPipeName(recorded)
		if err != nil || mapped != `\\.\pipe\`+recorded {
			t.Fatalf("original namespace mapping changed: %q %v", mapped, err)
		}
	}
	for _, invalid := range []string{"", "bad\x00name"} {
		if _, err := herdrPipeName(invalid); err == nil {
			t.Fatal("invalid pipe name accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if conn, err := dialHerdrSocket(ctx, `C:\nonexistent\wb-cancelled-fixture.sock`); err == nil || conn != nil {
		t.Fatalf("cancelled original pipe dial: %v", err)
	}
}
