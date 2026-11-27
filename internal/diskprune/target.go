package diskprune

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/diskusage"
)

// TargetKind is how a free-space target is expressed.
type TargetKind string

const (
	TargetRatio TargetKind = "ratio"
	TargetBytes TargetKind = "bytes"
	TargetMax   TargetKind = "max"
)

// Target is --until-free: a share of each filesystem, an amount of free
// space, or max (no target: everything eligible).
type Target struct {
	Kind  TargetKind
	Ratio float64
	Bytes int64
}

var sizeUnits = []struct {
	suffix string
	factor int64
}{
	{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
	{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1},
}

// ParseTarget reads "15%", "20GiB", "500MB" or "max".
func ParseTarget(raw string) (Target, error) {
	value := strings.TrimSpace(raw)
	if strings.EqualFold(value, "max") {
		return Target{Kind: TargetMax}, nil
	}
	if number, ok := strings.CutSuffix(value, "%"); ok {
		percent, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
		// NaN fails every comparison, so it is rejected explicitly: it would
		// otherwise be a target no filesystem ever meets.
		if err != nil || math.IsNaN(percent) || percent <= 0 || percent >= 100 {
			return Target{}, fmt.Errorf("--until-free %q: a percentage must be between 0 and 100 (exclusive)", raw)
		}
		return Target{Kind: TargetRatio, Ratio: percent / 100}, nil
	}
	for _, unit := range sizeUnits {
		number, ok := strings.CutSuffix(value, unit.suffix)
		if !ok {
			continue
		}
		amount, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
		bytes := amount * float64(unit.factor)
		if err != nil || math.IsNaN(amount) || amount <= 0 || bytes >= math.MaxInt64 {
			break
		}
		return Target{Kind: TargetBytes, Bytes: int64(bytes)}, nil
	}
	return Target{}, fmt.Errorf("--until-free %q: use a percentage such as 15%%, a size such as 20GiB, or max", raw)
}

// String renders the target as the user would type it.
func (t Target) String() string {
	switch t.Kind {
	case TargetMax:
		return "max"
	case TargetBytes:
		return diskusage.Human(t.Bytes)
	default:
		return strconv.FormatFloat(t.Ratio*100, 'f', -1, 64) + "%"
	}
}

// Met reports whether fs already has the free space the target asks for. A
// max target is never met: it means "everything eligible".
func (t Target) Met(fs disk.Filesystem) bool {
	switch t.Kind {
	case TargetMax:
		return false
	case TargetBytes:
		return fs.AvailableBytes >= t.Bytes
	default:
		return fs.TotalBytes > 0 && fs.AvailableRatio() >= t.Ratio
	}
}

// Unreachable reports a byte target larger than the whole filesystem: no
// amount of pruning can meet it, so the filesystem is reported, not emptied.
func (t Target) Unreachable(fs disk.Filesystem) bool {
	return t.Kind == TargetBytes && t.Bytes > fs.TotalBytes
}
