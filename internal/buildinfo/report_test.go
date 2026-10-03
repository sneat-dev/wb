package buildinfo

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSnapshotPreservesBuildAndFleetMetadata(t *testing.T) {
	t.Cleanup(func() { Set("") })
	for _, version := range []string{Unknown, "1.2.3"} {
		Set(version)
		got := Snapshot()
		fleet := JSON()
		if got.Version != version || got.JSONVersion != fleet.Version || got.Revision != Revision() || got.Modified != Modified() || got.Built != Date() || got.Go != runtime.Version() || got.Platform != runtime.GOOS+"/"+runtime.GOARCH || got.Name != fleet.Name || got.Commit != fleet.Commit || got.Date != fleet.Date || got.DateSource != fleet.DateSource {
			t.Fatalf("snapshot=%+v fleet=%+v", got, fleet)
		}
	}
}

func TestSnapshotAuxiliaryVersionIsNotSerialized(t *testing.T) {
	t.Parallel()
	info := Report{JSONVersion: "private-placeholder"}
	j, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	y, err := yaml.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{string(j), string(y)} {
		if strings.Contains(encoded, "private-placeholder") {
			t.Fatal(encoded)
		}
	}
}
