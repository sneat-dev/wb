package hubaddress

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestValidAcceptsHTTPSAndLoopbackHTTPOrigins(t *testing.T) {
	t.Parallel()
	for _, accepted := range []string{
		"https://hub.example", "https://hub.example/", "https://hub.example:8443", " https://hub.example ", "https://HUB.example",
		"http://127.0.0.1:8766", "http://localhost:8766", "http://localhost", "http://[::1]:8766", "http://127.0.0.1:8766/",
		// A capitalised loopback name is the same host; it is used in lower case.
		"http://LOCALHOST:8766", "http://Localhost:8766",
	} {
		if !Valid(accepted) {
			t.Errorf("Valid(%q) = false, want it accepted", accepted)
		}
	}
}

func TestValidRefusesEverythingACredentialMustNotBeSentTo(t *testing.T) {
	t.Parallel()
	for _, refused := range []string{
		"", "hub.example", "http://hub.example", "http://10.0.0.5:8766", "ftp://hub.example", "https://", "https://:8443",
		"https://user@hub.example", "https://user:secret@hub.example", "https://hub.example/path", "https://hub.example//",
		"https://hub.example?query=1", "https://hub.example?", "https://hub.example#fragment", "https://hub.example#", "https://hub.example/#",
		"http://127.0.0.1:8766/v0", "://bad", "https://hub.example/%zz",
		"http://localhost.:8766", "http://localhost.example:8766",
	} {
		if Valid(refused) {
			t.Errorf("Valid(%q) = true, want it refused", refused)
		}
	}
}

func TestIsLoopbackHostKnowsTheNameAndTheAddresses(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "127.8.9.1": true, "::1": true,
		"LOCALHOST": false, "Localhost": false, "10.0.0.1": false, "hub.example": false, "": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestOriginIsLowerCaseWithNoTrailingSlash(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"https://Hub.Example/": "https://hub.example", " HTTPS://HUB.example:8443 ": "https://hub.example:8443",
		"http://127.0.0.1:8766": "http://127.0.0.1:8766", "://bad": "", "http://Localhost:8766": "http://localhost:8766",
	} {
		if got := Origin(raw); got != want {
			t.Errorf("Origin(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestProxyNeverCarriesAPlainHTTPRequestAndTunnelsHTTPS proves the proxy policy
// against a real proxy, with an environment that names it for every request (as
// HTTP_PROXY and HTTPS_PROXY set to it would): a request to a loopback host by
// any spelling, and any plain http request, goes straight to its host and the
// proxy sees nothing of it; an https request reaches the proxy only as a
// CONNECT, which carries no Authorization header.
func TestProxyNeverCarriesAPlainHTTPRequestAndTunnelsHTTPS(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		proxied = append(proxied, request.Method+" "+request.Host+" authorization="+request.Header.Get("Authorization"))
		mu.Unlock()
		writer.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)
	everything := func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	client := &http.Client{Transport: &http.Transport{Proxy: ProxyFrom(everything)}}

	var direct []string
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		mu.Lock()
		direct = append(direct, request.Header.Get("Authorization"))
		mu.Unlock()
	}))
	t.Cleanup(target.Close)
	port := target.URL[strings.LastIndex(target.URL, ":"):]
	// Each of these names the target. The capitalised spellings are refused by
	// Valid before a request is built; the policy holds for them all the same.
	for _, address := range []string{"http://127.0.0.1" + port, "http://localhost" + port, "http://LOCALHOST" + port, "http://Localhost" + port} {
		request, _ := http.NewRequest(http.MethodGet, address+"/v0/workbench/machines/export", nil)
		request.Header.Set("Authorization", "Bearer the-credential")
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s: %v", address, err)
		}
		_ = response.Body.Close()
	}
	mu.Lock()
	if len(proxied) != 0 || len(direct) != 4 {
		t.Fatalf("loopback requests: the proxy saw %v and the host %d of 4", proxied, len(direct))
	}
	mu.Unlock()

	// The policy itself, for every shape of address.
	policy := ProxyFrom(everything)
	for address, wantProxy := range map[string]bool{
		"http://127.0.0.1:8766/x": false, "http://[::1]:8766/x": false, "http://localhost:8766/x": false, "http://LOCALHOST:8766/x": false,
		"http://hub.example/x": false, "https://localhost:8443/x": false, "https://LOCALHOST:8443/x": false, "https://127.0.0.1:8443/x": false,
		"https://[::1]:8443/x": false, "https://hub.example/x": true, "https://HUB.example:8443/x": true,
	} {
		request, _ := http.NewRequest(http.MethodGet, address, nil)
		got, err := policy(request)
		if err != nil || (got != nil) != wantProxy {
			t.Errorf("%s: proxy %v, %v, want proxied %v", address, got, err, wantProxy)
		}
	}

	// An https host is reached through the proxy as a tunnel: the proxy sees a
	// CONNECT with no credential, and nothing else.
	request, _ := http.NewRequest(http.MethodGet, "https://hub.example/v0/workbench/machines/export", nil)
	request.Header.Set("Authorization", "Bearer the-credential")
	if response, err := client.Do(request); err == nil {
		_ = response.Body.Close()
		t.Fatal("the request through a proxy that refuses the tunnel succeeded")
	}
	mu.Lock()
	defer mu.Unlock() //nolint:paralleltest // the last statement of the test
	if len(proxied) != 1 || proxied[0] != "CONNECT hub.example:443 authorization=" {
		t.Errorf("the proxy saw %v, want one CONNECT with no credential", proxied)
	}
}

func TestProxyIsThePolicyOverTheEnvironment(t *testing.T) {
	t.Parallel()
	request, _ := http.NewRequest(http.MethodGet, "http://hub.example/x", nil)
	if got, err := Proxy(request); got != nil || err != nil {
		t.Errorf("a plain http request = %v, %v, want no proxy whatever the environment says", got, err)
	}
}
