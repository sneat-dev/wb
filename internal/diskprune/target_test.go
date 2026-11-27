package diskprune

import (
	"math"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/disk"
)

func TestParseTargetForms(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]Target{
		"15%":                  {Kind: TargetRatio, Ratio: 0.15},
		" 20GiB ":              {Kind: TargetBytes, Bytes: 20 << 30},
		"500MB":                {Kind: TargetBytes, Bytes: 500e6},
		"1.5TiB":               {Kind: TargetBytes, Bytes: 3 << 39},
		"64B":                  {Kind: TargetBytes, Bytes: 64},
		"1.1KiB":               {Kind: TargetBytes, Bytes: 1126},
		"9223372036854775807B": {Kind: TargetBytes, Bytes: math.MaxInt64},
		"MAX":                  {Kind: TargetMax},
	} {
		got, err := ParseTarget(raw)
		if err != nil || got != want {
			t.Errorf("ParseTarget(%q) = %#v, %v; want %#v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "0%", "100%", "x%", "12XB", "-5GiB", "GiB", "soon",
		"0.5B", "9223372036854775808B", "8EiB", "1/2GiB", "1e3GB", "NaNGB"} {
		if _, err := ParseTarget(raw); err == nil || !strings.Contains(err.Error(), "--until-free") {
			t.Errorf("ParseTarget(%q) error = %v", raw, err)
		}
	}
}

func TestTargetMetAndUnreachable(t *testing.T) {
	t.Parallel()
	fs := disk.Filesystem{TotalBytes: 1000, AvailableBytes: 150}
	ratio, bytes, max := Target{Kind: TargetRatio, Ratio: 0.1}, Target{Kind: TargetBytes, Bytes: 200}, Target{Kind: TargetMax}
	if !ratio.Met(fs) || bytes.Met(fs) || max.Met(fs) {
		t.Fatal("met misjudged")
	}
	if ratio.Met(disk.Filesystem{}) {
		t.Fatal("an unmeasured filesystem never meets a ratio")
	}
	if !(Target{Kind: TargetBytes, Bytes: 2000}).Unreachable(fs) || bytes.Unreachable(fs) || ratio.Unreachable(fs) {
		t.Fatal("unreachable misjudged")
	}
	if ratio.String() != "10%" || max.String() != "max" || !strings.Contains(bytes.String(), "200") {
		t.Fatalf("strings: %s %s %s", ratio, max, bytes)
	}
}
