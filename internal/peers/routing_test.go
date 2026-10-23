package peers

import (
	"net/http"
	"strings"
	"testing"
)

func TestHandlerRejectsPathOutsideMountWithJSONError(t *testing.T) {
	t.Parallel()
	handler := NewHandler("/api/v1/peers", fakeSource{detailFound: true}, nil)
	response := request(t, handler, http.MethodGet, "/api/v1/peers-suffix")
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "peer_not_found") || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("outside-mount response = %d %s %q", response.Code, response.Body.String(), response.Header().Get("Content-Type"))
	}
}
