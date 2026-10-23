package layout

import (
	"reflect"
	"runtime"
	"testing"
)

func TestMigrationBusyProcessCapabilityPreservesReportNotes(t *testing.T) {
	t.Parallel()
	for _, supported := range []bool{true, false} {
		name := "unsupported"
		if supported {
			name = "supported"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			report := MigrateReport{Notes: []string{"existing migration observation"}}
			appendBusyProcessNote(&report, supported)
			want := []string{"existing migration observation"}
			if !supported {
				want = append(want, "busy-process check skipped: not supported on "+runtime.GOOS)
			}
			if !reflect.DeepEqual(report.Notes, want) {
				t.Fatalf("migration notes: got %q, want %q", report.Notes, want)
			}
		})
	}
}
