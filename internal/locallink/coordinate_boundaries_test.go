package locallink

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkAndUndoRetainPerInvocationAbsoluteFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"library", "consumer", "undo first", "undo retry"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, map[string]string{"backend/go.mod": goLibraryModule}, map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
			failure := errors.New("absolute coordinate unavailable")
			consumerCalls := 0
			absolute := func(path string) (string, error) {
				if path == f.consumer {
					consumerCalls++
				}
				if phase == "library" && path == f.library || phase == "consumer" && path == f.consumer || phase == "undo first" && path == f.consumer && consumerCalls == 1 || phase == "undo retry" && path == f.consumer && consumerCalls == 2 {
					return "", failure
				}
				return filepath.Abs(path)
			}
			options := Options{Library: f.library, Consumers: []string{f.consumer}, Stream: "fixture"}
			if strings.HasPrefix(phase, "undo") {
				_, err := f.engine.undoWithAbsolute(context.Background(), options, absolute)
				if !errors.Is(err, failure) {
					t.Fatalf("undo error=%v calls=%d", err, consumerCalls)
				}
			} else {
				result, err := f.engine.linkWithAbsolute(context.Background(), options, absolute)
				if phase == "library" {
					if !errors.Is(err, failure) {
						t.Fatalf("library error=%v", err)
					}
				} else if err != nil || len(result.Consumers) != 1 || len(result.Consumers[0].Errors) != 1 || !strings.Contains(result.Consumers[0].Errors[0], failure.Error()) {
					t.Fatalf("consumer refusal=%+v %v", result, err)
				}
			}
			if len(f.node.linked) != 0 {
				t.Fatalf("failed coordinate applied links: %v", f.node.linked)
			}
		})
	}
}

func TestLocalLinkCoordinateRefusalsPreserveNativeBoundaryErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("absolute coordinate unavailable")
	consumer, target, _, stage := lgCovStagedConsumer(t, "@acme/core")
	if _, err := validateStagedLinkPathWithAbsolute(consumer, target, stage, func(string) (string, error) { return "", failure }); !errors.Is(err, failure) {
		t.Fatalf("staged path error=%v", err)
	}
	engine := &Engine{}
	resolved, unrecordable, err := engine.resolveConsumerStreamsWithAbsolute(Options{Consumers: []string{consumer}}, func(string) (string, error) { return "", failure })
	if resolved != nil || unrecordable != nil || !errors.Is(err, failure) {
		t.Fatalf("consumer resolution=%v %v %v", resolved, unrecordable, err)
	}
}
