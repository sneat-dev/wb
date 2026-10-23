package sessiontransport

import (
	"errors"
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestLoadOverridePreservesStrictDecodingAcrossParsedNodeShapes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body  string
		want        Kind
		found       bool
		errContains string
	}{
		{name: "flow mapping", body: "session: {transport: tmux}\n", want: KindTmux, found: true},
		{name: "tagged mapping", body: "session: !!map {transport: tmux}\n", want: KindTmux, found: true},
		{name: "anchored mapping", body: "session: &override {transport: tmux}\n", want: KindTmux, found: true},
		{name: "external alias", body: "defaults: &override {transport: tmux}\nsession: *override\n", errContains: "unknown anchor"},
		{name: "custom tag", body: "session: !override {transport: tmux}\n", want: KindTmux, found: true},
		{name: "escaped scalar", body: "session: {transport: \"tmux\\t\"}\n", want: Kind("tmux\t"), found: true},
		{name: "empty mapping", body: "session: {}\n"},
		{name: "sequence", body: "session: [tmux]\n", errContains: "session section"},
		{name: "scalar", body: "session: tmux\n", errContains: "session section"},
		{name: "unknown field", body: "session: {transport: tmux, transprot: tmux}\n", errContains: "field transprot not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, found, err := LoadOverride(writeConfig(t, tc.body))
			if tc.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) || found || got != "" {
					t.Fatalf("LoadOverride = %q, %v, %v", got, found, err)
				}
				return
			}
			if err != nil || got != tc.want || found != tc.found {
				t.Fatalf("LoadOverride = %q, %v, %v; want %q, %v", got, found, err, tc.want, tc.found)
			}
		})
	}
}

func TestLoadOverrideReportsSerializationFailure(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session: {transport: tmux}\n")
	failure := errors.New("serialization failed")
	calls := 0
	got, found, err := loadOverrideWithMarshal(path, func(value any) ([]byte, error) {
		calls++
		node, ok := value.(*yaml.Node)
		if !ok || node.Kind != yaml.MappingNode || len(node.Content) != 2 || node.Content[1].Value != "tmux" {
			t.Fatalf("serialization input = %#v; want parsed session mapping", value)
		}
		return nil, failure
	})
	if got != "" || found || !errors.Is(err, failure) || !strings.Contains(err.Error(), "parse config "+path) || calls != 1 {
		t.Fatalf("serialization failure = %q, %v, %v; calls=%d", got, found, err, calls)
	}
}
