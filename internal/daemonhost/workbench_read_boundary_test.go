package daemonhost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
)

func TestMountedWorkbenchReadUsesTheLocalMemberViewer(t *testing.T) {
	t.Parallel()
	mount, err := mountHub(t.Context(), memoryHubConfig(t), "127.0.0.1:0", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub=%v %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	// The actual mounted read model is private-classed. Its successful read
	// requires the production local member resolver, without an owner bearer.
	response := httptest.NewRecorder()
	mount.Mounts[hub.APIPrefix+"/"].ServeHTTP(response, httptest.NewRequest(http.MethodGet, githubapp.APIPrefix+"/dashboard", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("private member read status=%d: %s", response.Code, response.Body.String())
	}
	var dashboard githubapp.Dashboard
	if err := json.Unmarshal(response.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.Summary.Repositories != 0 {
		t.Fatalf("empty actual snapshot store dashboard=%+v", dashboard)
	}
}
