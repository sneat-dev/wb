package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// everyRequestIsTheOwner is the authoriser of a test about what the owner is
// served; nobodyIsTheOwner is the one of a test about a refusal.
func everyRequestIsTheOwner(*http.Request) bool { return true }

func nobodyIsTheOwner(*http.Request) bool { return false }

// logMarker is planted in a fake runtime log. It must never reach a response to
// a request that is not the owner's.
const logMarker = "SENTINEL-LOG-CONTENT-7f3a"

// plantedLog writes a runtime log holding logMarker, in a directory whose name
// is a second marker, so a leaked path is caught too.
func plantedLog(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "SENTINEL-LOG-DIR-91c2")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte("ssh: "+logMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// refusal decodes a log route error body, which must be the API's JSON error
// shape and never stored.
func refusal(t *testing.T, recorder *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", got)
	}
	var body struct {
		SchemaVersion int    `json:"schema_version"`
		Error         string `json:"error"`
		Message       string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.SchemaVersion != APISchemaVersion {
		t.Fatalf("body = %q (%v), want the JSON error shape", recorder.Body.String(), err)
	}
	return body.Error, body.Message
}

// cockpit#ac:daemon-log-needs-an-owner-session: a request
// the authoriser does not call the owner's is refused 401 with a closed code,
// whatever its query, and the log file is never opened.
func TestLogRefusesARequestThatIsNotTheOwnersWithoutOpeningTheFile(t *testing.T) {
	t.Parallel()
	server := &service{options: Options{LogPath: plantedLog(t), Owner: nobodyIsTheOwner}}
	for _, target := range []string{"/api/v1/log", "/api/v1/log?tail=9", "/api/v1/log?tail=not-a-number"} {
		opened := 0
		recorder := httptest.NewRecorder()
		server.logOpened(recorder, httptest.NewRequest(http.MethodGet, target, nil), func(path string) (*os.File, error) {
			opened++
			return os.Open(path)
		})
		code, message := refusal(t, recorder)
		if recorder.Code != http.StatusUnauthorized || code != "owner_session_required" || !strings.Contains(message, "wb cockpit") {
			t.Errorf("%s = %d %q %q, want 401 owner_session_required naming the sign-in command", target, recorder.Code, code, message)
		}
		if opened != 0 {
			t.Errorf("%s opened the log file %d times for a request that is not the owner's", target, opened)
		}
		if recorder.Header().Get("X-Log-Truncated") != "" {
			t.Errorf("%s: a refusal carries X-Log-Truncated", target)
		}
	}
}

// cockpit#ac:daemon-log-fails-closed-without-an-owner-check: a handler built
// with no authoriser refuses everyone and opens nothing.
func TestLogRefusesEveryRequestWhenNoOwnerCheckIsConfigured(t *testing.T) {
	t.Parallel()
	server := &service{options: Options{LogPath: plantedLog(t)}}
	opened := 0
	recorder := httptest.NewRecorder()
	server.logOpened(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log", nil), func(path string) (*os.File, error) {
		opened++
		return os.Open(path)
	})
	code, _ := refusal(t, recorder)
	if recorder.Code != http.StatusForbidden || code != "log_owner_check_unavailable" || opened != 0 {
		t.Fatalf("no authoriser = %d %q after %d opens, want 403 log_owner_check_unavailable and no open", recorder.Code, code, opened)
	}
	if strings.Contains(recorder.Body.String(), "SENTINEL-LOG") {
		t.Fatal("the log content or its path reached a handler with no owner check")
	}
}

// cockpit#ac:daemon-log-needs-an-owner-session, as a
// sentinel: the marker planted in the log, and the marker in its path, reach no
// response to an anonymous request on any spelling of the route, through the
// whole handler; the owner is served the marker.
func TestLogSentinelNeverReachesAnAnonymousResponse(t *testing.T) {
	t.Parallel()
	path := plantedLog(t)
	const ownerCookie = "the-owner"
	handler := NewHandler(Options{Version: "test", LogPath: path, Owner: func(request *http.Request) bool {
		cookie, err := request.Cookie("session")
		return err == nil && cookie.Value == ownerCookie
	}})
	leaked := func(recorder *httptest.ResponseRecorder) bool {
		dump := recorder.Body.String()
		for name, values := range recorder.Header() {
			dump += name + strings.Join(values, "")
		}
		return strings.Contains(dump, "SENTINEL-LOG")
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, target := range []string{"/api/v1/log", "/api/v1/log?tail=1", "/api/v1/log?tail=4194304", "/api/v1/log?tail=x", "/api/v1/log/", "/api/v1/log/x"} {
			for name, cookie := range map[string]string{"no cookie": "", "another cookie": "not-the-owner"} {
				request := httptest.NewRequest(method, target, nil)
				if cookie != "" {
					request.AddCookie(&http.Cookie{Name: "session", Value: cookie})
				}
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if leaked(recorder) {
					t.Errorf("%s %s with %s leaked the log or its path: %d %q %v", method, target, name, recorder.Code, recorder.Body.String(), recorder.Header())
				}
				if request.URL.Path == "/api/v1/log" && recorder.Code != http.StatusUnauthorized {
					t.Errorf("%s %s with %s = %d, want 401", method, target, name, recorder.Code)
				}
			}
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/log", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: ownerCookie})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), logMarker) || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("the owner = %d %q, want the log, never stored", recorder.Code, recorder.Body.String())
	}
}

// cockpit#ac:daemon-log-error-names-no-path: the errors an open and a stat
// return name the file; neither reaches the body, even the owner's.
func TestLogFailureBodiesCarryAClosedCodeAndNoPath(t *testing.T) {
	t.Parallel()
	path := plantedLog(t)
	server := &service{options: Options{LogPath: path, Owner: everyRequestIsTheOwner}}
	opens := map[string]func(string) (*os.File, error){
		"open": func(string) (*os.File, error) {
			return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
		},
		"stat": func(path string) (*os.File, error) {
			file, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			return file, file.Close()
		},
	}
	for name, open := range opens {
		recorder := httptest.NewRecorder()
		server.logOpened(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log", nil), open)
		code, message := refusal(t, recorder)
		if recorder.Code != http.StatusServiceUnavailable || code != "log_unavailable" || message != logUnreadable {
			t.Errorf("%s failure = %d %q %q, want 503 log_unavailable and the fixed message", name, recorder.Code, code, message)
		}
		if body := recorder.Body.String(); strings.Contains(body, "SENTINEL-LOG") || strings.Contains(body, "daemon.log") {
			t.Errorf("%s failure body names the file: %q", name, body)
		}
	}
}
