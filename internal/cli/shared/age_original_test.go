package shared

import (
	"testing"
	"time"
)

func TestCwCovHumanAgeAndPublishedAgo(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		age  time.Duration
		want string
	}{
		{0, "just now"},
		{59 * time.Second, "just now"},
		{90 * time.Second, "1m"},
		{2 * time.Hour, "2h"},
		{47 * time.Hour, "47h"},
		{72 * time.Hour, "3d"},
	} {
		if got := HumanAge(test.age); got != test.want {
			t.Errorf("HumanAge(%v) = %q, want %q", test.age, got, test.want)
		}
	}
	if got := PublishedAgo("just now"); got != "just now" {
		t.Errorf("PublishedAgo(just now) = %q", got)
	}
	if got := PublishedAgo("2h"); got != "2h ago" {
		t.Errorf("PublishedAgo(2h) = %q", got)
	}
}
