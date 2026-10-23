package main

import (
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/spf13/cobra"
)

func newWorktreeOwnCmd() *cobra.Command {
	command := cmdworktree.NewOwn(cmdworktree.OwnOperations{Admission: requireMutationAdmission, Record: checkoutsetup.NewOwnership(checkoutsetup.DefaultOwnershipDependencies()).Record})
	addMutationAdmissionFlags(command)
	return command
}
