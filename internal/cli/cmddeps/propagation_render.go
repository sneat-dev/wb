package cmddeps

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/locallink"
)

func printPropagateLocal(out io.Writer, format string, result locallink.Result) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	// The verb states which checks it will run before reporting what they
	// found, so a caller never has to infer the plan from the outcome.
	if len(result.Plan) > 0 {
		if _, err := fmt.Fprintln(out, "plan:"); err != nil {
			return err
		}
		for _, step := range result.Plan {
			if _, err := fmt.Fprintf(out, "  - %s\n", step); err != nil {
				return err
			}
		}
	}
	if result.ContentHash != "" {
		state := "clean"
		if result.Dirty {
			state = "dirty"
		}
		if _, err := fmt.Fprintf(out, "\nlibrary %s at content-hash %s (%s)\n", result.Library, result.ContentHash, state); err != nil {
			return err
		}
		for _, identity := range result.Identities {
			if _, err := fmt.Fprintf(out, "  publishes %s %s (%s)\n", identity.Ecosystem, identity.Name, identity.Manifest); err != nil {
				return err
			}
		}
	}
	for _, consumer := range result.Consumers {
		if _, err := fmt.Fprintf(out, "\n%s\n", consumer.Consumer); err != nil {
			return err
		}
		if consumer.Skipped {
			if _, err := fmt.Fprintf(out, "  skipped: %s\n", consumer.Reason); err != nil {
				return err
			}
			continue
		}
		for _, link := range consumer.Links {
			if _, err := fmt.Fprintf(out, "  linked %s via %s (was %s)\n", link.Identity, link.Mechanism, link.PreviousVersion); err != nil {
				return err
			}
		}
		for _, skipped := range consumer.SkippedChecks {
			if _, err := fmt.Fprintf(out, "  ? not checked: %s\n", skipped); err != nil {
				return err
			}
		}
		if consumer.Verification != nil {
			if _, err := fmt.Fprintf(out, "  %s\n", consumer.Verification.Statement); err != nil {
				return err
			}
			for _, active := range consumer.Verification.ActiveLinks {
				if _, err := fmt.Fprintf(out, "  active link: %s\n", active); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "  linked run: passed=%t %s\n", consumer.Verification.Linked.Passed, consumer.Verification.Linked.Command); err != nil {
				return err
			}
			for _, detail := range consumer.Verification.Linked.Details {
				if _, err := fmt.Fprintf(out, "    ! %s\n", detail); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "  published baseline: passed=%t %s\n", consumer.Verification.PublishedBaseline.Passed, consumer.Verification.PublishedBaseline.Command); err != nil {
				return err
			}
			for _, detail := range consumer.Verification.PublishedBaseline.Details {
				if _, err := fmt.Fprintf(out, "    ! %s\n", detail); err != nil {
					return err
				}
			}
		}
		for _, note := range consumer.Notes {
			if _, err := fmt.Fprintf(out, "  %s\n", note); err != nil {
				return err
			}
		}
		for _, failure := range consumer.Errors {
			if _, err := fmt.Fprintf(out, "  ! %s\n", failure); err != nil {
				return err
			}
		}
	}
	if !result.Failed() && len(result.Consumers) > 0 && result.ContentHash != "" {
		if _, err := fmt.Fprintf(out, "\nwhile these links are live, do not run `go mod tidy` or `go get`: both resolve against the workspace.\nclear them with `wb deps propagate local --to %s --undo`.\n",
			strings.Join(consumerPaths(result), " --to ")); err != nil {
			return err
		}
	}
	return nil
}

func consumerPaths(result locallink.Result) []string {
	paths := make([]string, 0, len(result.Consumers))
	for _, consumer := range result.Consumers {
		if consumer.Skipped {
			continue
		}
		paths = append(paths, consumer.Consumer)
	}
	return paths
}
