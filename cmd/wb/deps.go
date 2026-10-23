package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmddeps"
	"github.com/sneat-dev/wb/internal/depsrun"
)

func newDepsCmd(inv *invocation) *cobra.Command {
	command := cmddeps.New(newCLIRuntime(inv), cmddeps.Operations(newDependencyService(), openBrowser))
	command.AddCommand(cmddeps.NewPublish(newCLIRuntime(inv), cmddeps.PublicationOperations(newPublicationService(inv))), cmddeps.NewPropagate(newCLIRuntime(inv), cmddeps.PropagationOperations(newPropagationService())), cmddeps.NewPolicy(newCLIRuntime(inv), cmddeps.PolicyOperations(newPolicyService())), cmddeps.NewGoDirective(newCLIRuntime(inv), cmddeps.DirectiveOperations(newDependencyService())))
	return command
}
func newDependencyService() *depsrun.Service {
	return depsrun.New(depsrun.DefaultDependencies(os.Stderr))
}

func newPolicyService() *depsrun.PolicyService {
	return depsrun.NewPolicy(depsrun.DefaultPolicyDependencies(os.Stderr), usageError)
}

func newPropagationService() *depsrun.PropagationService {
	return depsrun.NewPropagation(depsrun.DefaultPropagationDependencies())
}

func newPublicationService(inv *invocation) *depsrun.PublicationService {
	return depsrun.NewPublication(depsrun.DefaultPublicationDependencies(os.Stderr), newCLIRuntime(inv).ExitError)
}
