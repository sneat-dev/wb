package cockpit

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGzipRoundTripsAndPayloadCompressesOnce(t *testing.T) {
	t.Parallel()
	body := []byte(strings.Repeat(`{"a":1}`, 200))
	reader, err := gzip.NewReader(bytes.NewReader(Gzip(body)))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(reader); !bytes.Equal(plain, body) {
		t.Fatal("Gzip does not round-trip")
	}
	var compressed atomic.Int64
	payload := NewPayload(body, func(data []byte) []byte {
		compressed.Add(1)
		return Gzip(data)
	})
	for range 50 {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Accept-Encoding", "gzip")
		ServePayload(httptest.NewRecorder(), request, payload)
		ServePayload(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), payload)
	}
	if compressed.Load() != 1 {
		t.Errorf("the compressor ran %d times", compressed.Load())
	}
	if payload.tag == "" || !strings.HasPrefix(payload.tag, `"`) || strings.Contains(payload.tag, "gzip") {
		t.Errorf("tag %q", payload.tag)
	}
}

func TestServePayloadChoosesTheEncodingAndMatchesEitherTag(t *testing.T) {
	t.Parallel()
	payload := NewPayload([]byte(`{"ok":true}`+"\n"), Gzip)
	identityTag := payload.tag
	gzipTag := payload.gzipTag()
	do := func(headers ...string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		for index := 0; index < len(headers); index += 2 {
			request.Header.Add(headers[index], headers[index+1])
		}
		recorder := httptest.NewRecorder()
		ServePayload(recorder, request, payload)
		return recorder
	}
	plain := do()
	if plain.Header().Get("ETag") != identityTag || plain.Header().Get("Content-Encoding") != "" || plain.Header().Get("Vary") != "Origin, Accept-Encoding" || plain.Header().Get("Cache-Control") != "no-cache" || plain.Body.String() != `{"ok":true}`+"\n" {
		t.Errorf("identity = %v", plain.Header())
	}
	zipped := do("Accept-Encoding", "gzip")
	if zipped.Header().Get("ETag") != gzipTag || !strings.HasSuffix(gzipTag, `-gzip"`) || zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("gzip = %v", zipped.Header())
	}
	for _, candidate := range []string{identityTag, gzipTag, "W/" + gzipTag, `"x", ` + identityTag, " * "} {
		for _, accept := range []string{"", "gzip"} {
			if recorder := do("Accept-Encoding", accept, "If-None-Match", candidate); recorder.Code != http.StatusNotModified || recorder.Body.Len() != 0 || recorder.Header().Get("Content-Encoding") != "" {
				t.Errorf("If-None-Match %q with %q = %d", candidate, accept, recorder.Code)
			}
		}
	}
	if recorder := do("If-None-Match", `"other"`); recorder.Code != 200 {
		t.Errorf("a stale tag = %d", recorder.Code)
	}
}

func TestAcceptsGzipReadsTheHeaderLikeAClient(t *testing.T) {
	t.Parallel()
	for accept, want := range map[string]bool{
		"gzip": true, "GZIP": true, "gzip, deflate, br": true, "deflate, gzip;q=0.5": true, " gzip ; q=1 ": true, "*": true, "*;q=0.1": true,
		"identity": false, "br": false, "": false, "gzip;q=0": false, "gzip;q=0.0": false, "*;q=0": false, "gzip;q=bad": true, "gzip;level=1": true,
	} {
		if got := acceptsGzip([]string{accept}); got != want {
			t.Errorf("Accept-Encoding %q = %v, want %v", accept, got, want)
		}
	}
	if !acceptsGzip([]string{"br", "gzip"}) || acceptsGzip(nil) {
		t.Error("separate header lines are not read as one list")
	}
}
