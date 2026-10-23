//go:build unix

package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLogReportsUnseekablePipe(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wb.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	server := &service{options: Options{LogPath: path, Owner: everyRequestIsTheOwner}}
	response := httptest.NewRecorder()
	server.logOpened(response, httptest.NewRequest(http.MethodGet, "/api/v1/log", nil), func(path string) (*os.File, error) { return os.OpenFile(path, os.O_RDWR, 0) })
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "log_unavailable") {
		t.Fatalf("seek failure response: %d %s", response.Code, response.Body.String())
	}
}
