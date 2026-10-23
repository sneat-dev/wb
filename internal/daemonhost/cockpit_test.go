package daemonhost

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func TestCockpitRegisterFleetServesTheWarmingDocumentBeforeTheFirstSnapshot(t *testing.T) {
	t.Parallel()
	const address = "127.0.0.1:8766"
	server := newCockpitServer(address, wbconfig.DefaultCockpitConfig())
	snapshotter := registerCockpitFleet(server, cockpitfleet.Options{})
	api := server.Mounts()[cockpit.APIPrefix]
	for target, want := range map[string]int{"/api/v1/cockpit/fleet": http.StatusOK, cockpitfleet.ReadmePath + "?repository=x": http.StatusUnauthorized} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Host = address
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Errorf("%s = %d %s, want %d", target, recorder.Code, recorder.Body.String(), want)
		}
	}
	if body := fleetBody(snapshotter); !strings.Contains(string(body), `"warming_up":true`) {
		t.Error("a snapshotter that has not started is not warming up")
	}
}

func TestCockpitServerKeepsSessionsInMemorySoARestartEndsThem(t *testing.T) {
	t.Parallel()
	const address = "127.0.0.1:8766"
	var key string
	session := func(server *cockpit.Server, cookie *http.Cookie) string {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/cockpit/session", nil)
		request.Host = address
		request.AddCookie(cookie)
		request.Header.Set(cockpit.SessionKeyHeader, key)
		recorder := httptest.NewRecorder()
		server.Mounts()[cockpit.APIPrefix].ServeHTTP(recorder, request)
		return recorder.Body.String()
	}
	config := wbconfig.DefaultCockpitConfig()
	first := newCockpitServer(address, config)
	issued, err := first.MintLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	key = issued.Key
	request := httptest.NewRequest(http.MethodGet, cockpit.LoginPath+"?code="+issued.Code, nil)
	request.Host = address
	recorder := httptest.NewRecorder()
	first.Mounts()[cockpit.PagePrefix].ServeHTTP(recorder, request)
	cookies := recorder.Result().Cookies()
	if recorder.Code != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatalf("login = %d with %d cookies", recorder.Code, len(cookies))
	}
	if body := session(first, cookies[0]); !strings.Contains(body, `"principal":"owner"`) {
		t.Fatalf("session on the run that set it = %q", body)
	}
	restarted := newCockpitServer(address, config)
	if body := session(restarted, cookies[0]); !strings.Contains(body, `"principal":"anonymous-local"`) || strings.Contains(body, "repo.content.read") {
		t.Fatalf("session after a restart = %q, want anonymous-local", body)
	}
}
