package hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/hubaddress"
)

// TestProviderNeverSendsItsCredentialThroughAnHTTPProxy proves the hub client
// follows the proxy policy of a client that sends a machine credential: with an
// environment that names a proxy for every request, a loopback http hub is
// reached directly with the bearer and the proxy sees nothing; the default
// client has that policy; and an https origin is used in lower case.
func TestProviderNeverSendsItsCredentialThroughAnHTTPProxy(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	proxied, authorized := 0, 0
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		proxied++
		mu.Unlock()
	}))
	t.Cleanup(proxy.Close)
	hubServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		if request.Header.Get("Authorization") == "Bearer the-credential" {
			authorized++
		}
		mu.Unlock()
		_, _ = writer.Write([]byte(`{"snapshots":[]}`))
	}))
	t.Cleanup(hubServer.Close)
	proxyURL, _ := url.Parse(proxy.URL)
	everything := func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	port := hubServer.URL[strings.LastIndex(hubServer.URL, ":"):]
	for _, address := range []string{"http://127.0.0.1" + port, "http://localhost" + port} {
		provider, err := New(Options{BaseURL: address, Machine: "laptop", Token: "the-credential", Client: newClient(hubaddress.ProxyFrom(everything))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.List(t.Context()); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	mu.Lock()
	if proxied != 0 || authorized != 2 {
		t.Errorf("the proxy saw %d requests and the hub %d of 2 with the bearer", proxied, authorized)
	}
	mu.Unlock()

	provider, err := New(Options{BaseURL: " https://Hub.Example/ ", Machine: "laptop", Token: "the-credential"})
	if err != nil {
		t.Fatal(err)
	}
	transport, isTransport := provider.client.Transport.(*http.Transport)
	if !isTransport || reflect.ValueOf(transport.Proxy).Pointer() != reflect.ValueOf(hubaddress.Proxy).Pointer() || provider.client.Timeout == 0 {
		t.Errorf("the default client does not follow the credential proxy policy: %+v", provider.client)
	}
	if provider.baseURL != "https://hub.example" {
		t.Errorf("baseURL = %q, want the origin in lower case", provider.baseURL)
	}
	// A capitalised localhost is the same host: accepted, used in lower case and
	// never proxied.
	capital, err := New(Options{BaseURL: "http://Localhost" + port, Machine: "laptop", Token: "the-credential", Client: newClient(hubaddress.ProxyFrom(everything))})
	if err != nil || capital.baseURL != "http://localhost"+port {
		t.Fatalf("http://Localhost = %v, %v", capital, err)
	}
	if _, err := capital.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if proxied != 0 || authorized != 3 {
		t.Errorf("after the capitalised address the proxy saw %d requests and the hub %d of 3", proxied, authorized)
	}
	mu.Unlock()
	// A default transport of another type does not make the client panic.
	other := credentialTransport(roundTripFunc(nil), hubaddress.Proxy)
	if other == nil || reflect.ValueOf(other.Proxy).Pointer() != reflect.ValueOf(hubaddress.Proxy).Pointer() {
		t.Error("a replaced default transport lost the proxy policy")
	}
}
