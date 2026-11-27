package wbconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
)

const deskMachine = "0123456789abcdef0123456789abcdef"

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDeskSectionAbsentMeansNotConnected(t *testing.T) {
	t.Parallel()
	missing, err := LoadDesk(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil || missing.Connected() || missing.URL != desk.DefaultURL || missing.Detail != desk.LevelRepositories {
		t.Fatalf("missing file = %+v, %v", missing, err)
	}
	other, err := LoadDesk(writeConfig(t, "remote:\n  provider: git\n"))
	if err != nil || other.Connected() || other.PublishMinInterval != DefaultDeskPublishMinInterval || other.HeartbeatInterval != DefaultDeskHeartbeatInterval {
		t.Fatalf("no desk section = %+v, %v", other, err)
	}
	empty, err := LoadDesk(writeConfig(t, "desk:\n"))
	if err != nil || empty.Connected() {
		t.Fatalf("empty desk section = %+v, %v", empty, err)
	}
}

func TestDeskSectionParsesEveryField(t *testing.T) {
	t.Parallel()
	config, err := LoadDesk(writeConfig(t, `desk:
  url: http://127.0.0.1:8080/
  machine_id: `+deskMachine+`
  space_id: personal1
  shared_space_ids: [team1]
  credential_file: /home/me/.config/wb/credentials/desk-mac.token
  detail: full
  publish:
    min_interval: 10s
  heartbeat:
    interval: 2m
`))
	if err != nil {
		t.Fatal(err)
	}
	if !config.Connected() || config.URL != "http://127.0.0.1:8080" || config.Detail != desk.LevelFull ||
		config.SpaceID != "personal1" || len(config.SharedSpaceIDs) != 1 ||
		config.PublishMinInterval != MinDeskPublishMinInterval || config.HeartbeatInterval != 2*time.Minute {
		t.Fatalf("config = %+v", config)
	}
	disabled, err := LoadDesk(writeConfig(t, "desk:\n  enabled: false\n  machine_id: "+deskMachine+"\n  credential_file: /x\n  heartbeat:\n    interval: 1s\n  publish: {}\n"))
	if err != nil || disabled.Connected() || !disabled.Installed() || disabled.HeartbeatInterval != MinDeskHeartbeatInterval || disabled.PublishMinInterval != DefaultDeskPublishMinInterval {
		t.Fatalf("disabled = %+v, %v", disabled, err)
	}
}

func TestDeskSectionRefusesUnsafeOrUnknownValues(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"not yaml":          "desk: [",
		"unknown key":       "desk:\n  token: abc\n",
		"plain http":        "desk:\n  url: http://desk.sneat.dev\n",
		"bad machine id":    "desk:\n  machine_id: XYZ\n",
		"bad space":         "desk:\n  space_id: a/b\n",
		"bad shared space":  "desk:\n  shared_space_ids: ['x y']\n",
		"relative file":     "desk:\n  credential_file: desk.token\n",
		"summary detail":    "desk:\n  detail: summary\n",
		"bad publish":       "desk:\n  publish:\n    min_interval: soon\n",
		"negative interval": "desk:\n  heartbeat:\n    interval: -1m\n",
	} {
		if _, err := LoadDesk(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	unreadable := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDesk(unreadable); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("a directory as config = %v", err)
	}
}

func TestSetDeskOwnsOnlyItsKeysAndClearDeskRemovesThem(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "# my settings\nremote:\n  provider: git\ndesk:\n  enabled: true\n  heartbeat:\n    interval: 10m\n  shared_space_ids: [old]\n")
	connection := DeskConnection{URL: "https://desk.sneat.dev", MachineID: deskMachine, SpaceID: "personal1",
		SharedSpaceIDs: []string{"team1", "team2"}, CredentialFile: "/c/desk.token", Detail: desk.LevelRepositories}
	if err := SetDesk(path, connection); err != nil {
		t.Fatal(err)
	}
	config, err := LoadDesk(path)
	if err != nil || !config.Connected() || config.HeartbeatInterval != 10*time.Minute || strings.Join(config.SharedSpaceIDs, ",") != "team1,team2" {
		t.Fatalf("after SetDesk = %+v, %v", config, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# my settings") || !strings.Contains(string(raw), "provider: git") || strings.Contains(string(raw), "old") {
		t.Fatalf("other settings must survive and stale shares must go:\n%s", raw)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	connection.SharedSpaceIDs = nil
	if err := SetDesk(path, connection); err != nil {
		t.Fatal(err)
	}
	if err := SetDeskDetail(path, desk.LevelFull); err != nil {
		t.Fatal(err)
	}
	if config, _ = LoadDesk(path); config.Detail != desk.LevelFull || len(config.SharedSpaceIDs) != 0 {
		t.Fatalf("after detail change = %+v", config)
	}
	if err := ClearDesk(path); err != nil {
		t.Fatal(err)
	}
	if config, _ = LoadDesk(path); config.Connected() || config.HeartbeatInterval != 10*time.Minute {
		t.Fatalf("ClearDesk keeps the operator's settings only: %+v", config)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "machine_id") || !strings.Contains(string(raw), "enabled") {
		t.Fatalf("after ClearDesk:\n%s", raw)
	}
}

func TestClearDeskRemovesAnEmptiedSectionAndToleratesAbsence(t *testing.T) {
	t.Parallel()
	if err := ClearDesk(filepath.Join(t.TempDir(), "none.yaml")); err != nil {
		t.Fatalf("a missing file is already clear: %v", err)
	}
	if err := ClearDesk(writeConfig(t, "remote:\n  provider: git\n")); err != nil {
		t.Fatalf("a missing section is already clear: %v", err)
	}
	path := writeConfig(t, "desk:\n  url: https://desk.sneat.dev\n  machine_id: "+deskMachine+"\n")
	if err := ClearDesk(path); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "desk") {
		t.Fatalf("an emptied section is removed:\n%s", raw)
	}
	scalarSection := writeConfig(t, "desk: off\n")
	if err := ClearDesk(scalarSection); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(scalarSection); strings.Contains(string(raw), "desk") {
		t.Fatalf("a non-mapping section is removed:\n%s", raw)
	}
	if err := ClearDesk(writeConfig(t, "- a list\n")); err == nil {
		t.Fatal("a non-mapping file must be refused")
	}
}

func TestDeskWritersRefuseAMalformedFile(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"list": "- a\n", "scalar desk": "desk: off\n"} {
		path := writeConfig(t, body)
		if err := SetDesk(path, DeskConnection{}); err == nil {
			t.Errorf("SetDesk on %s accepted", name)
		}
		if err := SetDeskDetail(path, desk.LevelFull); err == nil {
			t.Errorf("SetDeskDetail on %s accepted", name)
		}
	}
	if err := SetDeskDetail(filepath.Join(t.TempDir(), "new.yaml"), desk.LevelFull); err != nil {
		t.Fatalf("SetDeskDetail creates the file: %v", err)
	}
}
