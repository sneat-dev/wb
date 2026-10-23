package reposelection

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestForEachSerialOrderAndZeroWork(t *testing.T) {
	t.Parallel()
	var indices []int
	ForEach(4, 1, func(index int) { indices = append(indices, index) })
	if !reflect.DeepEqual(indices, []int{0, 1, 2, 3}) {
		t.Fatalf("indices=%v", indices)
	}
	ForEach(0, 0, func(int) { t.Fatal("zero work called callback") })
}

func TestForEachRejectsInvalidNonemptyWork(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		count, parallel int
		want            string
	}{{-1, 1, "reposelection.ForEach: count must not be negative"}, {1, 0, "reposelection.ForEach: parallelism must be at least 1"}, {1, -2, "reposelection.ForEach: parallelism must be at least 1"}} {
		t.Run(fmt.Sprintf("%d_%d", tc.count, tc.parallel), func(t *testing.T) {
			t.Parallel()
			defer func() {
				if got := recover(); got != tc.want {
					t.Fatalf("panic=%v want=%s", got, tc.want)
				}
			}()
			ForEach(tc.count, tc.parallel, func(int) { t.Fatal("invalid work called callback") })
			t.Fatal("invalid work did not panic")
		})
	}
}

func TestForEachBoundsCallbacksRunsEachIndexOnceAndJoins(t *testing.T) {
	t.Parallel()
	for _, parallel := range []int{1, 2, 4, 20} {
		t.Run(fmt.Sprint(parallel), func(t *testing.T) {
			t.Parallel()
			const count = 7
			var active, peak atomic.Int32
			calls := make([]atomic.Int32, count)
			entered := make(chan int, count)
			release := make(chan struct{})
			releaseCallbacks := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseCallbacks)
			done := make(chan struct{})
			go func() {
				ForEach(count, parallel, func(index int) {
					calls[index].Add(1)
					n := active.Add(1)
					for old := peak.Load(); n > old; old = peak.Load() {
						if peak.CompareAndSwap(old, n) {
							break
						}
					}
					entered <- index
					<-release
					active.Add(-1)
				})
				close(done)
			}()
			bound := min(count, parallel)
			for range bound {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("workers failed to start")
				}
			}
			select {
			case <-done:
				t.Fatal("dispatcher returned before callbacks completed")
			default:
			}
			if got := peak.Load(); got != int32(bound) {
				t.Fatalf("peak=%d want=%d", got, bound)
			}
			releaseCallbacks()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("dispatcher failed to join")
			}
			if active.Load() != 0 {
				t.Fatal("callback still active after join")
			}
			for index := range count {
				if got := calls[index].Load(); got != 1 {
					t.Fatalf("index %d called %d times", index, got)
				}
			}
		})
	}
}
