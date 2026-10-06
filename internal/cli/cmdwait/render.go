package cmdwait

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/waitrun"
	"io"
	"strings"
	"time"
)

func waitResumeArgs(output waitrun.Output, condition waitrun.Condition, slice, interval time.Duration, jsonOut bool) []string {
	args := []string{"wb", "wait", "pr"}
	for _, target := range output.Targets {
		if target.Status != waitrun.Settled {
			args = append(args, target.Selector)
		}
	}
	args = append(args, "--until", string(condition), "--slice", slice.String(), "--interval", interval.String())
	if jsonOut {
		args = append(args, "--json")
	}
	return args
}

// printWaitFailures prints why a target is red, closest-to-the-cause first: a
// file and line if GitHub annotated one, otherwise a bounded log excerpt. This
// is the whole point of waking an agent for a failure rather than a link.
func printWaitFailures(out io.Writer, target waitrun.Target) error {
	for _, failure := range target.Failures {
		for _, annotation := range failure.Annotations {
			location := annotation.Path
			if annotation.StartLine > 0 {
				location = fmt.Sprintf("%s:%d", annotation.Path, annotation.StartLine)
			}
			if _, err := fmt.Fprintf(out, "  %s: %s: %s\n", failure.Check, location, annotation.Message); err != nil {
				return err
			}
		}
		if len(failure.Annotations) == 0 {
			detail := failure.Excerpt
			if strings.TrimSpace(detail) == "" {
				detail = failure.Reason
			}
			if strings.TrimSpace(detail) == "" {
				continue
			}
			for _, excerptLine := range strings.Split(strings.TrimRight(detail, "\n"), "\n") {
				if _, err := fmt.Fprintf(out, "  %s: %s\n", failure.Check, excerptLine); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func printWaitOutput(out io.Writer, output waitrun.Output) error {
	for _, target := range output.Targets {
		line := fmt.Sprintf("%s %s", target.Selector, target.Status)
		if target.State != "" {
			line += " state=" + target.State
		}
		if len(target.Checks) > 0 {
			line += " checks=" + waitrun.ChecksKey(target.Checks)
		}
		if len(target.Failed) > 0 {
			line += " failed=" + strings.Join(target.Failed, ",")
		}
		if len(target.Blocked) > 0 {
			line += " blocked=" + strings.Join(target.Blocked, ",")
		}
		if target.Reason != "" {
			line += " (" + target.Reason + ")"
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
		if err := printWaitFailures(out, target); err != nil {
			return err
		}
		for _, missing := range target.Blocked {
			if _, err := fmt.Fprintf(out, "  required check %q has no passing result on this head; it cannot merge until something produces it\n", missing); err != nil {
				return err
			}
		}
	}
	if len(output.ResumeArgs) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(output.ResumeArgs))
	for _, argument := range output.ResumeArgs {
		quoted = append(quoted, shared.ShellQuoteArg(argument))
	}
	_, err := fmt.Fprintf(out, "resume: %s\n", strings.Join(quoted, " "))
	return err
}
