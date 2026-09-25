package dashboard

// Pack unit p02 coverage: service.log's ?tail= clamp to maxLogTailBytes.
// Uses a real (but local, in TempDir) log file so the clamp is observable
// in the returned byte count and X-Log-Truncated header.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPkp02ServiceLogTailClamped(t *testing.T) {
	t.Parallel()
	const fileSize = 5 * 1024 * 1024 // bigger than maxLogTailBytes (4 MiB)
	logPath := filepath.Join(t.TempDir(), "wb.log")
	if err := os.WriteFile(logPath, []byte(strings.Repeat("a", fileSize)), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	server := &service{options: Options{LogPath: logPath}}
	// Requested tail exceeds maxLogTailBytes (4<<20); without the clamp the
	// computed seek start would be negative and truncated would read false.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/log?tail=6291456", nil)
	recorder := httptest.NewRecorder()

	server.log(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Log-Truncated"); got != "true" {
		t.Fatalf("expected truncated=true once tail is clamped, got %q", got)
	}
	const maxLogTailBytesWant = 4 << 20
	if recorder.Body.Len() != maxLogTailBytesWant {
		t.Fatalf("expected exactly %d clamped bytes, got %d", maxLogTailBytesWant, recorder.Body.Len())
	}
}
