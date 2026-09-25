package agentguard

// Pack unit p02 coverage: parseSpliceCandidate's trailing-whitespace guard.

import "testing"

func TestPkp02ParseSpliceCandidateWhitespaceOnly(t *testing.T) {
	t.Parallel()
	words, hadCdPrefix, ok := parseSpliceCandidate("    ")
	if !ok {
		t.Fatalf("expected ok=true for a whitespace-only command")
	}
	if hadCdPrefix {
		t.Fatalf("expected hadCdPrefix=false")
	}
	if len(words) != 0 {
		t.Fatalf("expected no words, got %v", words)
	}
}
