package main

import "github.com/sneat-dev/wb/internal/cli/shared"

// newCLIRuntime bridges the remaining invocation-based constructors to command
// families. The getter observes parsed flags and pre-run policy lazily.
func newCLIRuntime(inv *invocation) shared.Runtime {
	runtime := newCLIErrorRuntime()
	runtime.Flags = func() shared.Flags {
		return shared.Flags{
			ProjectsRoot:   inv.projectsRoot,
			Filter:         inv.filterFlag,
			ExtraOrgs:      append([]string(nil), inv.extraOrgs...),
			NonInteractive: inv.nonInteractive,
			Quiet:          inv.quiet,
		}
	}
	return runtime
}

// newCLIErrorRuntime supplies the root error contract to families that read no invocation flags.
func newCLIErrorRuntime() shared.Runtime {
	return shared.Runtime{ExitError: func(code int, message string) error { return &exitError{code: code, message: message} }}
}
