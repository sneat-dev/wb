package mechanicalchange

import (
	"testing"
)

func TestOrchCovLimitStringsReportsTheRemainder(t *testing.T) {
	t.Parallel()
	values := []string{"a", "b", "c"}
	if got := limitStrings(values, 5); len(got) != 3 {
		t.Fatalf("short list = %v", got)
	}
	got := limitStrings(values, 2)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "and 1 more" {
		t.Fatalf("limited list = %v", got)
	}
}
