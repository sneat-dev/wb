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
