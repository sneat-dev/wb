package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogReportsClosedFileStatFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wb.log")
	if err := os.WriteFile(path, []byte("complete log"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &service{options: Options{LogPath: path}}
	response := httptest.NewRecorder()
	server.logOpened(response, httptest.NewRequest(http.MethodGet, "/api/v1/log", nil), func(path string) (*os.File, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return file, nil
	})
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "log_unavailable") {
		t.Fatalf("stat failure response: %d %s", response.Code, response.Body.String())
	}
}
