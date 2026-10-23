package age

import (
	"testing"
	"time"
)

func TestHumanAgeAndPublishedAgoRetainCanonicalBoundaries(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		elapsed time.Duration
		want    string
	}{{-time.Second, "just now"}, {0, "just now"}, {time.Minute, "1m"}, {59 * time.Minute, "59m"}, {time.Hour, "1h"}, {47 * time.Hour, "47h"}, {48 * time.Hour, "2d"}} {
		if actual := HumanAge(row.elapsed); actual != row.want {
			t.Fatal(row, actual)
		}
		want := row.want + " ago"
		if row.want == "just now" {
			want = row.want
		}
		if actual := PublishedAgo(row.want); actual != want {
			t.Fatal(row, actual)
		}
	}
}
