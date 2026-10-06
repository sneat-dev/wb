package daemonhost

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/hub"
)

func TestDaemonHostedHubWritesNeedTheOwnersCredential(t *testing.T) {
	t.Parallel()
	mount, get, bearer, _ := exportTestMount(t, "127.0.0.1:8809")
	api := mount.handlers()[hub.APIPrefix+"/"]
	post := func(target, body string, headers ...string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		for index := 0; index < len(headers); index += 2 {
			request.Header.Set(headers[index], headers[index+1])
		}
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		return recorder
	}
	const coverage = `{"repository":"sneat-dev/wb","statements":10,"covered":9}`
	const metric = `{"repository":"sneat-dev/wb","metric_type":"commits_per_day","value":3}`
	writes := []struct{ path, body string }{
		{hub.CoveragePath, coverage},
		{hub.MetricsPath, metric},
		{hub.InstallationConnectPath, `{}`},
		{hub.InstallationAuthorizePath, `{"state":"s","challenge":"c"}`},
	}
	refuseAnonymous := func(when string) {
		t.Helper()
		for _, write := range writes {
			if response := post(write.path, write.body, "Origin", "https://sneat.work"); response.Code != http.StatusUnauthorized {
				t.Errorf("%s: anonymous POST %s = %d %s, want 401", when, write.path, response.Code, response.Body.String())
			}
		}
		for _, path := range []string{hub.CoveragePath, hub.MetricsPath + "?type=commits_per_day"} {
			if response := get(path); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
				t.Errorf("%s: anonymous GET %s = %d %s, want an empty list", when, path, response.Code, response.Body.String())
			}
		}
	}
	refuseAnonymous("before the owner check is bound")
	// A nil check binds nothing, and a daemon with no hub has nothing to bind.
	mount.authorizeOwnerWith(nil)
	(*hubMount)(nil).authorizeOwnerWith(func(*http.Request) bool { return true })
	mount.authorizeOwnerWith(func(request *http.Request) bool { return request.Header.Get("X-Test-Owner") == "live" })
	refuseAnonymous("with the owner check bound")

	if response := post(hub.CoveragePath, coverage, "Authorization", bearer); response.Code != http.StatusCreated {
		t.Fatalf("coverage by the owner's machine bearer = %d %s", response.Code, response.Body.String())
	}
	if response := post(hub.MetricsPath, metric, "X-Test-Owner", "live"); response.Code != http.StatusCreated {
		t.Fatalf("metric by the owner session = %d %s", response.Code, response.Body.String())
	}
	if response := get(hub.CoveragePath + "/sneat-dev/wb"); response.Code != http.StatusOK {
		t.Fatalf("stored coverage = %d %s", response.Code, response.Body.String())
	}
	if response := get(hub.MetricsPath + "?type=commits_per_day"); !strings.Contains(response.Body.String(), "sneat-dev/wb") {
		t.Fatalf("stored metric = %d %s", response.Code, response.Body.String())
	}
}
