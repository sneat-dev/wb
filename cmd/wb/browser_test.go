package main

import "testing"

func TestBrowserCommandUsesPlatformMechanism(t *testing.T) {
	t.Parallel()
	tests := []struct {
		goos, want string
	}{
		{"darwin", "open"}, {"linux", "xdg-open"}, {"windows", "rundll32"},
	}
	for _, test := range tests {
		name, args, err := browserCommand(test.goos, "/tmp/report.html")
		if err != nil {
			t.Fatal(err)
		}
		if name != test.want || len(args) == 0 || args[len(args)-1] != "/tmp/report.html" {
			t.Fatalf("browserCommand(%q) = %q %v", test.goos, name, args)
		}
	}
	if _, _, err := browserCommand("plan9", "/tmp/report.html"); err == nil {
		t.Fatal("unsupported platform was accepted")
	}
}
