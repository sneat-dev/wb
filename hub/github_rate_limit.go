package hub

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimit is what GitHub's X-RateLimit-* response headers said about the
// remaining budget and its next reset. Known is false when neither header was
// present or parseable, which every caller treats as "nothing to act on"
// rather than a zero budget.
//
// This is the one place that parsing exists: hub/poller and hub/redeliver
// both read GitHub REST responses and both need it, and a second copy would
// be exactly the kind of drift a shared seam is for.
type RateLimit struct {
	Known     bool
	Remaining int
	Reset     time.Time
}

// ReadRateLimit parses the X-RateLimit-Remaining and X-RateLimit-Reset
// response headers GitHub's REST API sends on almost every response.
func ReadRateLimit(header http.Header) RateLimit {
	remaining, remainingErr := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Remaining")))
	reset, resetErr := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64)
	if remainingErr != nil || resetErr != nil {
		return RateLimit{}
	}
	return RateLimit{Known: true, Remaining: remaining, Reset: time.Unix(reset, 0)}
}

// RetryAfter parses GitHub's Retry-After response header, in seconds. GitHub
// sends it on some rate-limited and abuse-detection responses, sometimes
// instead of the X-RateLimit-* headers above and sometimes alongside them; a
// caller that wants to honor both prefers this one when present.
func RetryAfter(header http.Header) (time.Duration, bool) {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}
