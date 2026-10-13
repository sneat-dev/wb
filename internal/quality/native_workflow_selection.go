package quality

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var nativeRunArgument = regexp.MustCompile(`-run(?:=|\s+)(?:'([^']*)'|"([^"]*)"|(\S+))`)

// ValidateNativeWorkflowSelector checks executable run steps, not comments or
// workflow metadata, against the selector used by native coverage execution.
func ValidateNativeWorkflowSelector(data []byte) error {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return fmt.Errorf("parse native workflow: %w", err)
	}
	found := false
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			for _, line := range strings.Split(step.Run, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "go run ./cmd/wb check ") {
					if err := validateNativeCheckCommand(line); err != nil {
						return err
					}
					found = true
					continue
				}
				if !strings.HasPrefix(line, "go test ") || (!strings.Contains(line, "-tags e2e") && !strings.Contains(line, "-tags=e2e")) {
					continue
				}
				found = true
				matches := nativeRunArgument.FindAllStringSubmatch(line, -1)
				if len(matches) > 1 {
					return fmt.Errorf("native workflow has duplicate -run arguments")
				}
				selector := ""
				if len(matches) == 1 {
					for _, argument := range matches[0][1:] {
						if argument != "" {
							selector = argument
						}
					}
				}
				if selector != NativeGoTestSelector {
					return fmt.Errorf("native workflow selector %q differs from %q", selector, NativeGoTestSelector)
				}
			}
		}
	}
	if !found {
		return fmt.Errorf("native workflow has no e2e Go test run step")
	}
	return nil
}

// The native profile owns tagged discovery and NativeGoTestSelector; its CLI
// contract tests bind this exact entrypoint to the shared runner. Only the full
// current repository and these presentation/budget options are admitted here.
func validateNativeCheckCommand(line string) error {
	fields := strings.Fields(line)
	if len(fields) < 7 || fields[4] != "." {
		return fmt.Errorf("native check must select the complete current repository")
	}
	seen := map[string]bool{}
	for i := 5; i < len(fields); i += 2 {
		if i+1 == len(fields) || seen[fields[i]] {
			return fmt.Errorf("ambiguous native check arguments")
		}
		seen[fields[i]] = true
		value := fields[i+1]
		switch fields[i] {
		case "--profile":
			if value != "native" {
				return fmt.Errorf("native check profile must be native")
			}
		case "--timeout":
			if value != "10m" {
				return fmt.Errorf("native check must retain its 10m attempt budget")
			}
		case "--format":
			if value != "json" {
				return fmt.Errorf("native check format must be json")
			}
		default:
			return fmt.Errorf("unsupported native check option %q", fields[i])
		}
	}
	if !seen["--profile"] {
		return fmt.Errorf("native check profile is missing")
	}
	return nil
}
