package qualityrun

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/quality"
	"strings"
)

func WorklistText(worklist quality.Worklist) string {
	var out strings.Builder
	_, _ = fmt.Fprintf(&out, "worklist: %d unit(s), %d uncovered statement(s) (target unit size %d)\n", len(worklist.Units), worklist.TotalUncoveredStatements, worklist.UnitSize)
	for _, unit := range worklist.Units {
		shares := ""
		if len(unit.SharesFileWith) > 0 {
			names := make([]string, len(unit.SharesFileWith))
			for i, index := range unit.SharesFileWith {
				names[i] = fmt.Sprintf("%d", index)
			}
			shares = fmt.Sprintf(" (shares a file with unit %s)", strings.Join(names, ", "))
		}
		_, _ = fmt.Fprintf(&out, "  unit %d: %d statement(s), %d file(s)%s\n", unit.Index, unit.Statements, len(unit.Files), shares)
		for _, file := range unit.Files {
			_, _ = fmt.Fprintf(&out, "    %s\n", file)
		}
		for _, block := range unit.Blocks {
			function := block.Function
			if function == "" {
				function = "(file-level)"
			}
			_, _ = fmt.Fprintf(&out, "    %s:%d.%d,%d.%d %d stmt(s) %s\n", block.File, block.StartLine, block.StartCol, block.EndLine, block.EndCol, block.Statements, function)
		}
	}
	return out.String()
}
