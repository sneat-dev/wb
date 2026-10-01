package hubaddress

import "testing"

func TestValidAcceptsHTTPSAndLoopbackHTTPOrigins(t *testing.T) {
	t.Parallel()
	for _, accepted := range []string{
		"https://hub.example", "https://hub.example/", "https://hub.example:8443", " https://hub.example ",
		"http://127.0.0.1:8766", "http://localhost:8766", "http://LOCALHOST", "http://[::1]:8766",
	} {
		if !Valid(accepted) {
			t.Errorf("Valid(%q) = false, want it accepted", accepted)
		}
	}
}

func TestValidRefusesEverythingACredentialMustNotBeSentTo(t *testing.T) {
	t.Parallel()
	for _, refused := range []string{
		"", "hub.example", "http://hub.example", "http://10.0.0.5:8766", "ftp://hub.example", "https://",
		"https://user@hub.example", "https://user:secret@hub.example", "https://hub.example/path",
		"https://hub.example?query=1", "https://hub.example#fragment", "http://127.0.0.1:8766/v0", "://bad", "https://hub.example/%zz",
	} {
		if Valid(refused) {
			t.Errorf("Valid(%q) = true, want it refused", refused)
		}
	}
}

func TestIsLoopbackHostKnowsNamesAndAddresses(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "127.8.9.1": true, "::1": true, "10.0.0.1": false, "hub.example": false, "": false} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}
