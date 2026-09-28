package sessioncustody

import (
	"testing"
	"time"
)

func TestNowUsesInjectedClock(t *testing.T) {
	t.Parallel()
	want := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	got := now(Options{Now: func() time.Time { return want }})
	if !got.Equal(want) {
		t.Fatalf("now() = %v, want %v", got, want)
	}
}

func TestNowFallsBackToRealClock(t *testing.T) {
	t.Parallel()
	before := time.Now().UTC()
	got := now(Options{})
	after := time.Now().UTC()
	if got.Before(before) || got.After(after) {
		t.Fatalf("now() = %v, want between %v and %v", got, before, after)
	}
	if got.Location() != time.UTC {
		t.Fatalf("now() location = %v, want UTC", got.Location())
	}
}
