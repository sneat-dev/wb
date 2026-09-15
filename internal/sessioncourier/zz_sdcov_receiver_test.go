package sessioncourier

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
)

func TestSDCovValidateReceiverRequestBranches(t *testing.T) {
	if _, err := validateReceiverRequest(nil, 1024); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty request error = %v", err)
	}
	if _, err := validateReceiverRequest([]byte("{}"), 1); err == nil || !strings.Contains(err.Error(), "exceeds 1 bytes") {
		t.Fatalf("oversized request error = %v", err)
	}
	if _, err := validateReceiverRequest([]byte("not-json"), 1024); err == nil || !strings.Contains(err.Error(), "parse session move request") {
		t.Fatalf("undecodable request error = %v", err)
	}

	request, raw := courierTestRequest(t)
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := validateReceiverRequest(compact.Bytes(), maxSSHRequestBytes); err == nil || !strings.Contains(err.Error(), "canonical JSON") {
		t.Fatalf("noncanonical request error = %v", err)
	}

	got, err := validateReceiverRequest(raw, maxSSHRequestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got != request {
		t.Fatalf("validated request = %#v, want %#v", got, request)
	}
}

func TestSDCovDecodeReceiverResultTrailingGarbage(t *testing.T) {
	request, raw := courierTestRequest(t)
	encoded := encodeCourierResult(t, validCourierResult(request, raw))
	encoded = append(encoded, []byte(" {")...)
	if _, err := decodeReceiverResult(encoded); err == nil || !strings.Contains(err.Error(), "trailing session receive output") {
		t.Fatalf("decodeReceiverResult error = %v, want trailing-decode refusal", err)
	}
}

func TestSDCovValidateReceiverResultFieldBranches(t *testing.T) {
	request, raw := courierTestRequest(t)
	tests := map[string]struct {
		mutate func(*sessionreceive.Result)
		want   string
	}{
		"invalid response request": {
			mutate: func(result *sessionreceive.Result) { result.Request = sessionmove.Request{} },
			want:   "response request is invalid",
		},
		"model mismatch": {
			mutate: func(result *sessionreceive.Result) { result.Successor.Model = "different-model" },
			want:   "successor model",
		},
		"worktree dir is relative": {
			mutate: func(result *sessionreceive.Result) { result.Successor.WorktreeDir = "relative/worktree" },
			want:   "not a clean absolute path",
		},
		"successor pid is not live": {
			mutate: func(result *sessionreceive.Result) { result.Successor.PID = 0 },
			want:   "one live started process",
		},
		"successor worktree dir differs from received worktree": {
			mutate: func(result *sessionreceive.Result) { result.Successor.WorktreeDir = "/elsewhere/worktree" },
			want:   "does not match received worktree",
		},
		"native harness identity mismatch": {
			mutate: func(result *sessionreceive.Result) {
				result.Successor.NativeHarnessID = "successor-harness"
				result.Receipt.NativeHarnessID = "receipt-harness"
			},
			want: "harness identity",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			result := validCourierResult(request, raw)
			test.mutate(&result)
			runner := &fakeCommandRunner{response: encodeCourierResult(t, result)}
			deliverer := newTestSSHDeliverer(t, sessionmove.SSHConfig{Host: "target"}, runner)
			if _, err := deliverer.Deliver(context.Background(), raw); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Deliver error = %v, want containing %q", err, test.want)
			}
			if runner.calls != 1 {
				t.Fatalf("ssh calls = %d, want no fallback", runner.calls)
			}
		})
	}
}
