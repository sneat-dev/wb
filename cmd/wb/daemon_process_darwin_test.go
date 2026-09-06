//go:build darwin

package main

import (
	"encoding/xml"
	"strings"
	"testing"
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
