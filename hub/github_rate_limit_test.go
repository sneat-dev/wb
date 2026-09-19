package hub

import (
	"net/http"
	"testing"
	"time"
)

func TestReadRateLimitParsesBothHeadersOrNeither(t *testing.T) {
	if ReadRateLimit(http.Header{}).Known {
		t.Fatal("absent headers must not be read as a known budget")
	}
	header := http.Header{}
	header.Set("X-RateLimit-Remaining", "10")
	if ReadRateLimit(header).Known {
		t.Fatal("a remaining count with no reset must not be read as known")
	}
	header.Set("X-RateLimit-Reset", "not-a-number")
	if ReadRateLimit(header).Known {
		t.Fatal("an unparsable reset must not be read as known")
	}
	header.Set("X-RateLimit-Reset", "1700000000")
	limit := ReadRateLimit(header)
	if !limit.Known || limit.Remaining != 10 || !limit.Reset.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("limit = %+v", limit)
	}
}

func TestRetryAfterParsesSecondsAndRejectsGarbage(t *testing.T) {
	if _, ok := RetryAfter(http.Header{}); ok {
		t.Fatal("an absent header must not report a wait")
	}
	header := http.Header{}
	header.Set("Retry-After", "not-a-number")
	if _, ok := RetryAfter(header); ok {
		t.Fatal("an unparsable value must not report a wait")
	}
	header.Set("Retry-After", "-5")
	if _, ok := RetryAfter(header); ok {
		t.Fatal("a negative value must not report a wait")
	}
	header.Set("Retry-After", "30")
	wait, ok := RetryAfter(header)
	if !ok || wait != 30*time.Second {
		t.Fatalf("wait = %s, %t", wait, ok)
	}
}
