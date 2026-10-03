package main

import "github.com/sneat-dev/wb/internal/cli/shared"

// newCLIRuntime bridges the remaining invocation-based constructors to command
// families. The getter observes parsed flags and pre-run policy lazily.
func newCLIRuntime(inv *invocation) shared.Runtime {
	return shared.Runtime{
		Flags: func() shared.Flags {
			return shared.Flags{
				ProjectsRoot:   inv.projectsRoot,
				Filter:         inv.filterFlag,
				ExtraOrgs:      append([]string(nil), inv.extraOrgs...),
				NonInteractive: inv.nonInteractive,
				Quiet:          inv.quiet,
			}
		},
		ExitError: func(code int, message string) error { return &exitError{code: code, message: message} },
	}
}
