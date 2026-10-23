package runqueue

import (
	"io"

	"github.com/spf13/pflag"
)

// classifyWBCoverage consumes leading root flags before recognizing the command.
// Coverage options remain conservatively admitted without duplicating their parser.
func classifyWBCoverage(arguments []string) Kind {
	flags := pflag.NewFlagSet("wb coverage admission", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.SetInterspersed(false)
	flags.String("projects-root", "", "")
	flags.String("filter", "", "")
	flags.StringArray("org", nil, "")
	flags.Bool("non-interactive", false, "")
	flags.Bool("quiet", false, "")
	help := flags.BoolP("help", "h", false, "")
	version := flags.BoolP("version", "v", false, "")
	if err := flags.Parse(arguments); err != nil {
		return KindNone
	}
	remaining := flags.Args()
	if len(remaining) == 0 || remaining[0] != "coverage" {
		return KindNone
	}
	if *version || (len(remaining) == 1 && *help) {
		return KindNone
	}
	if len(remaining) > 1 {
		switch remaining[1] {
		case "baseline", "summary", "worklist":
			return KindNone
		}
	}
	if len(remaining) == 2 {
		switch remaining[1] {
		case "--help", "-h", "--help=true", "--help=1", "-h=true", "-h=1":
			return KindNone
		}
	}
	return KindRaceOrCover
}
