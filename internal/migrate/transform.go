package migrate

import (
	"fmt"
	"strings"
)

func transform(steps []Step, language string, original []byte, path string) ([]byte, []string, error) {
	updated := original
	var applied []string
	for _, step := range steps {
		if step.Language != "" && step.Language != language {
			continue
		}
		var (
			next    []byte
			changed bool
			err     error
		)
		switch step.Kind {
		case "text.replace":
			next = []byte(strings.ReplaceAll(string(updated), step.From, step.To))
			changed = string(next) != string(updated)
		case "import.replace", "selector.rewrite", "selector.rename":
			adapter, ok := structuralAdapters[language]
			if !ok {
				return nil, nil, fmt.Errorf("%s for %s requires a structural adapter", step.Kind, language)
			}
			next, changed, err = adapter.Transform(updated, path, step)
		default:
			return nil, nil, fmt.Errorf("unsupported kind %q", step.Kind)
		}
		if err != nil {
			return nil, nil, err
		}
		if changed {
			updated = next
			applied = append(applied, step.Kind)
		}
	}
	return updated, applied, nil
}
