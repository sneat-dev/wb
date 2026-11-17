package agentmessage

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

func TestPendingWalkerVisitsBeforeNextBoundedPageAndCompletesTail(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	for range 103 {
		m := sampleMessage()
		m.ID = NewID()
		mustDeliver(t, s, m)
	}
	dir, err := s.directory("mac")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	visited, pages := 0, 0
	read := func(n int) ([]os.DirEntry, error) {
		if n != 100 {
			t.Fatalf("unbounded page: %d", n)
		}
		if pages > 0 && visited < 100 {
			t.Fatal("read next page before consuming prior bodies", visited)
		}
		pages++
		return dir.ReadDir(n)
	}
	if err := walkPending(context.Background(), dir, "mac", read, func(Message) (bool, error) { visited++; return true, nil }); err != nil {
		t.Fatal(err)
	}
	if visited != 103 || pages < 2 {
		t.Fatal("incomplete traversal", visited, pages)
	}
}

func TestPendingWalkerStopsWithoutReadingAfterCancellationOrCallbackFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"before-page", "during-page", "next-page", "callback-error", "callback-stop", "partial-eof"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			m := sampleMessage()
			mustDeliver(t, s, m)
			dir, err := s.directory(m.To)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = dir.Close() })
			entries, err := dir.ReadDir(1)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if mode == "before-page" {
				cancel()
			}
			reads, visits := 0, 0
			failure := errors.New("callback refused")
			read := func(n int) ([]os.DirEntry, error) {
				reads++
				if n != 100 || reads != 1 {
					t.Fatal("unexpected later or unbounded read", n, reads)
				}
				if mode == "during-page" {
					cancel()
				}
				if mode == "partial-eof" {
					return entries, io.EOF
				}
				return entries, nil
			}
			err = walkPending(ctx, dir, m.To, read, func(Message) (bool, error) {
				visits++
				switch mode {
				case "next-page":
					cancel()
				case "callback-error":
					return false, failure
				case "callback-stop":
					return false, nil
				}
				return true, nil
			})
			switch mode {
			case "before-page":
				if !errors.Is(err, context.Canceled) || reads != 0 || visits != 0 {
					t.Fatal(err, reads, visits)
				}
			case "during-page":
				if !errors.Is(err, context.Canceled) || reads != 1 || visits != 0 {
					t.Fatal(err, reads, visits)
				}
			case "next-page":
				if !errors.Is(err, context.Canceled) || visits != 1 {
					t.Fatal(err, visits)
				}
			case "callback-error":
				if !errors.Is(err, failure) || visits != 1 {
					t.Fatal(err, visits)
				}
			default:
				if err != nil || visits != 1 {
					t.Fatal(err, visits)
				}
			}
		})
	}
}
