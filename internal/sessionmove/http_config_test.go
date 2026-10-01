package sessionmove

import (
	"path/filepath"
	"strings"
	"testing"
)

const httpTargetPrefix = `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
      http:
`

// TestLoadConfigReadsTheHTTPSectionBesideSSH proves the optional http section
// of cockpit-views#req:remote-http-fetch: its url and token_file are read beside
// ssh, it is not a courier, and a target without it is unchanged.
func TestLoadConfigReadsTheHTTPSectionBesideSSH(t *testing.T) {
	t.Parallel()
	tokenFile := filepath.Join(t.TempDir(), "vm.token")
	config, err := LoadConfig(writeConfig(t, httpTargetPrefix+"        url: https://vm.example\n        token_file: "+tokenFile+`
    other:
      default_courier: ssh
      ssh:
        host: other
    hub:
      default_courier: ssh
      ssh:
        host: hub
      http:
        url: http://127.0.0.1:8766
`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	vm, _ := config.Target("vm")
	if vm.HTTP == nil || vm.HTTP.URL != "https://vm.example" || vm.HTTP.TokenFile != tokenFile {
		t.Fatalf("vm http = %+v", vm.HTTP)
	}
	if vm.DefaultCourier != CourierSSH || vm.SSH == nil {
		t.Errorf("the http section changed the courier: %+v", vm)
	}
	if other, _ := config.Target("other"); other.HTTP != nil {
		t.Errorf("a target with no http section has one: %+v", other.HTTP)
	}
	// A loopback http origin is allowed, and the token file may be left to the
	// reader (the hub this machine is enrolled with).
	if hub, _ := config.Target("hub"); hub.HTTP == nil || hub.HTTP.URL != "http://127.0.0.1:8766" || hub.HTTP.TokenFile != "" {
		t.Errorf("hub http = %+v", hub.HTTP)
	}
}

// TestLoadConfigRefusesAnHTTPAddressACredentialMustNotBeSentTo proves the URL
// rule of cockpit-views#ac:bearer-stays-with-the-configured-host at the
// configuration: plain http off loopback, user information, a path, a query and
// a fragment are refused, and so is a relative token file. The http section
// alone is no courier.
func TestLoadConfigRefusesAnHTTPAddressACredentialMustNotBeSentTo(t *testing.T) {
	t.Parallel()
	for name, section := range map[string]string{
		"plain http off loopback": "        url: http://vm.example\n",
		"user information":        "        url: https://user:secret@vm.example\n",
		"a path":                  "        url: https://vm.example/v0/workbench\n",
		"a query":                 "        url: https://vm.example?machine=mac\n",
		"a fragment":              "        url: https://vm.example#x\n",
		"no url":                  "        token_file: /etc/wb/vm.token\n",
		"relative token file":     "        url: https://vm.example\n        token_file: vm.token\n",
	} {
		_, err := LoadConfig(writeConfig(t, httpTargetPrefix+section))
		if err == nil || !strings.Contains(err.Error(), "session_move.targets.vm: http.") {
			t.Errorf("%s: LoadConfig = %v, want the http section refused", name, err)
		}
	}
	_, err := LoadConfig(writeConfig(t, `session_move:
  targets:
    vm:
      default_courier: http
      http:
        url: https://vm.example
`))
	if err == nil || !strings.Contains(err.Error(), "default_courier") {
		t.Errorf("an http-only target = %v, want it refused: http is not a courier", err)
	}
}
