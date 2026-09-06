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
