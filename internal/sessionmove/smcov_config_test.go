package sessionmove

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmCovConfigUnconfiguredErrorNamesPath(t *testing.T) {
	message := (&UnconfiguredError{Path: "/home/ai/.config/wb/wb.yaml"}).Error()
	if !strings.Contains(message, "/home/ai/.config/wb/wb.yaml") || !strings.Contains(message, "session_move.targets") {
		t.Fatalf("UnconfiguredError message = %q, want the exact path and the session_move.targets hint", message)
	}
}

func TestSmCovConfigLoadReportsAbsentAndMalformedFiles(t *testing.T) {
	t.Run("absent file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent.yaml")
		_, err := LoadConfig(missing)
		var unconfigured *UnconfiguredError
		if !errors.As(err, &unconfigured) || unconfigured.Path != missing {
			t.Fatalf("LoadConfig(absent) error = %v, want UnconfiguredError for %s", err, missing)
		}
	})

	t.Run("unreadable path", func(t *testing.T) {
		if _, err := LoadConfig(t.TempDir()); err == nil || !strings.Contains(err.Error(), "read config") {
			t.Fatalf("LoadConfig(directory) error = %v, want read error", err)
		}
	})

	t.Run("malformed yaml", func(t *testing.T) {
		if _, err := LoadConfig(writeConfig(t, "session_move: [")); err == nil || !strings.Contains(err.Error(), "parse config") {
			t.Fatalf("LoadConfig(malformed) error = %v, want parse error", err)
		}
	})

	t.Run("no session_move section", func(t *testing.T) {
		_, err := LoadConfig(writeConfig(t, "remote:\n  repo: sneat-dev/wb-state\n"))
		var unconfigured *UnconfiguredError
		if !errors.As(err, &unconfigured) {
			t.Fatalf("LoadConfig(no section) error = %v, want UnconfiguredError", err)
		}
	})

	t.Run("no targets", func(t *testing.T) {
		if _, err := LoadConfig(writeConfig(t, "session_move:\n  targets: {}\n")); err == nil || !strings.Contains(err.Error(), "at least one target") {
			t.Fatalf("LoadConfig(no targets) error = %v, want target requirement", err)
		}
	})

	t.Run("invalid machine name", func(t *testing.T) {
		body := "session_move:\n  targets:\n    \"-vm\":\n      default_courier: ssh\n      ssh:\n        host: vm\n"
		if _, err := LoadConfig(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "target machine") {
			t.Fatalf("LoadConfig(invalid machine) error = %v, want machine id error", err)
		}
	})
}

func TestSmCovConfigValidateTargetCourierSections(t *testing.T) {
	tests := []struct {
		name   string
		target TargetConfig
		want   string
	}{
		{"ssh without section", TargetConfig{DefaultCourier: CourierSSH}, "requires an ssh section"},
		{"synchestra without section", TargetConfig{DefaultCourier: CourierSynchestra}, "requires a synchestra section"},
		{"loopback courier", TargetConfig{DefaultCourier: CourierLoopback}, "must be"},
		{"unknown courier", TargetConfig{DefaultCourier: "carrier-pigeon"}, "must be"},
		{"empty runner", TargetConfig{DefaultCourier: CourierSynchestra, Synchestra: &SynchestraConfig{}}, "synchestra.runner is required"},
		{"whitespace runner", TargetConfig{DefaultCourier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "two words"}}, "whitespace"},
		{"nul runner", TargetConfig{DefaultCourier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "run\x00ner"}}, "NUL"},
		{"extra synchestra section is still validated", TargetConfig{
			DefaultCourier: CourierSSH,
			SSH:            &SSHConfig{Host: "vm"},
			Synchestra:     &SynchestraConfig{Runner: "--all"},
		}, "option-like"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTarget(test.target)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateTarget error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSmCovConfigSSHUserIsValidatedWithExplicitWBPath(t *testing.T) {
	valid := SSHConfig{Host: "vm", User: "ai_1", WBPath: "/opt/wb"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid ssh config with explicit wb_path: %v", err)
	}
	invalid := SSHConfig{Host: "vm", User: "ai;touch", WBPath: "/opt/wb"}
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "ssh.user") {
		t.Fatalf("invalid ssh user with explicit wb_path error = %v", err)
	}
}
