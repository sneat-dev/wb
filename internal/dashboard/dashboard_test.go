package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardServesUIAndHealth(t *testing.T) {
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "1.2.3"})

	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: "WB operations"},
		{path: "/api/v1/health", contentType: "application/json", contains: `"status":"ready"`},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", test.path, response.Code)
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, test.contentType) {
			t.Errorf("%s content type = %q", test.path, contentType)
		}
		if !strings.Contains(response.Body.String(), test.contains) {
			t.Errorf("%s body does not contain %q", test.path, test.contains)
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s omitted security headers", test.path)
		}
	}
}
