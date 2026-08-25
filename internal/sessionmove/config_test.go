package sessionmove

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigReadsCanonicalMachineTargets(t *testing.T) {
	path := writeConfig(t, `remote:
  repo: sneat-dev/wb-state
session_move:
  targets:
    hetzner-vm1:
      default_courier: ssh
      ssh:
        host: hetzner-vm1
        wb_path: /home/ai/go/bin/wb
      synchestra:
        runner: hetzner-vm1
`)

	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	target, ok := config.Target("hetzner-vm1")
	if !ok {
		t.Fatal("canonical target is missing")
	}
	if target.Machine != "hetzner-vm1" || target.DefaultCourier != CourierSSH || target.SSH == nil ||
		target.SSH.Host != "hetzner-vm1" || target.SSH.WBPath != "/home/ai/go/bin/wb" ||
		target.Synchestra == nil || target.Synchestra.Runner != "hetzner-vm1" {
		t.Fatalf("target = %+v", target)
	}
}

func TestLoadConfigRejectsUnsafeCourierArguments(t *testing.T) {
	tests := map[string]string{
		"option-like ssh host": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: -oProxyCommand=bad
`,
		"whitespace ssh host": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: "vm one"
`,
		"ssh host shell separator": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: "vm;touch"
`,
		"ssh host variable expansion": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: 'vm$HOME'
`,
		"ssh host command substitution": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: 'vm` + "`id`" + `'
`,
		"ssh host glob": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: 'vm*'
`,
		"relative wb path": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
        wb_path: bin/wb
`,
		"whitespace wb path": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
        wb_path: "/opt/wb current/wb"
`,
		"wb path shell separator": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
        wb_path: "/opt/wb;touch"
`,
		"wb path variable expansion": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
        wb_path: '/opt/$HOME/wb'
`,
		"wb path command substitution": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
        wb_path: '/opt/` + "`id`" + `/wb'
`,
		"wb path quote": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
        wb_path: "/opt/wb'bad"
`,
		"wb path backslash": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
        wb_path: '/opt/wb\\bad'
`,
		"wb path glob": `session_move:
  targets:
    vm:
      default_courier: ssh
      ssh:
        host: vm
        wb_path: '/opt/wb*'
`,
		"option-like runner": `session_move:
  targets:
    vm:
      default_courier: synchestra
      synchestra:
        runner: --all
`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, body)); err == nil {
				t.Fatal("LoadConfig accepted unsafe target configuration")
			}
		})
	}
}

func TestSSHConfigAllowsFixedCommandOrShellInertAbsolutePath(t *testing.T) {
	for _, config := range []SSHConfig{
		{Host: "hetzner-vm1"},
		{Host: "hetzner.vm_1", WBPath: "/home/ai/go/bin/wb-v1.2+local"},
	} {
		if err := config.Validate(); err != nil {
			t.Fatalf("SSHConfig.Validate(%+v): %v", config, err)
		}
	}
}

func TestLoadConfigRequiresConfiguredDefaultCourier(t *testing.T) {
	path := writeConfig(t, `session_move:
  targets:
    vm:
      default_courier: synchestra
      ssh:
        host: vm
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "default_courier") {
		t.Fatalf("LoadConfig error = %v, want missing default courier configuration", err)
	}
}
