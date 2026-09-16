//go:build darwin

package main

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestLaunchdPlistUsesLaunchdDictionaryAndEscapesArguments(t *testing.T) {
	data := launchdPlistBytes("/tmp/wb&candidate", []string{"--projects-root", "/tmp/a<b"}, "/tmp/wb.log")
	text := string(data)
	if !strings.Contains(text, "<dict>") || !strings.Contains(text, "<key>ProgramArguments</key><array>") || strings.Contains(text, "<Dictionary>") {
		t.Fatalf("plist does not use launchd dictionary shape: %s", text)
	}
	var document struct {
		XMLName xml.Name `xml:"plist"`
	}
	if err := xml.Unmarshal(data, &document); err != nil {
		t.Fatalf("plist XML is invalid: %v", err)
	}
	if !strings.Contains(text, "/tmp/wb&amp;candidate") || !strings.Contains(text, "/tmp/a&lt;b") {
		t.Fatalf("plist arguments are not escaped: %s", text)
	}
}

func TestLaunchdPIDFromAuthoritativeJobState(t *testing.T) {
	output := "gui/501/dev.sneat.wb.daemon = {\n\tstate = running\n\tpid = 65918\n}"
	if pid, ok := launchdPIDFromOutput(output); !ok || pid != 65918 {
		t.Fatalf("launchd pid = %d, %t", pid, ok)
	}
	if pid, ok := launchdPIDFromOutput("state = exited\nlast exit code = 1"); ok || pid != 0 {
		t.Fatalf("stopped launchd job reported pid = %d, %t", pid, ok)
	}
}

// A unit written while a WB_HOME was set must carry that input, not a runtime
// path resolved from it: a resolved path outlives the home it was resolved
// from, which is how a daemon ended up serving an abandoned directory.
func TestLaunchdPlistPinsWBHomeAndNoResolvedRuntimePath(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, "/tmp/wb-home-fixture")
	data := launchdPlistBytes("/tmp/wb", []string{"--projects-root", "/tmp/projects", "daemon", "serve", "--listen", daemonDefaultListen, "--managed-start"}, "/Users/someone/Library/Logs/wb/daemon.log")
	text := string(data)
	if !strings.Contains(text, "<key>EnvironmentVariables</key><dict><key>WB_HOME</key><string>/tmp/wb-home-fixture</string></dict>") {
		t.Fatalf("plist does not pin WB_HOME: %s", text)
	}
	for _, forbidden := range []string{"runtime", "daemon-state.json", "--lifecycle-state"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("plist pins %q: %s", forbidden, text)
		}
	}

	t.Setenv(wbhome.EnvOverride, "")
	plain := string(launchdPlistBytes("/tmp/wb", []string{"--projects-root", "/tmp/projects", "daemon", "serve"}, "/tmp/wb.log"))
	if strings.Contains(plain, "EnvironmentVariables") {
		t.Fatalf("an unset WB_HOME must not be invented: %s", plain)
	}
}
