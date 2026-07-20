// Command wb is the workbench CLI: fleet-wide operations across the user's
// GitHub repositories, plus repo-sync.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	projectsRoot string
	filterFlag   string
	extraOrgs    []string
)

func main() {
	home, _ := os.UserHomeDir()
	root := &cobra.Command{
		Use:           "wb",
		Short:         "Workbench CLI — fleet-wide operations across your GitHub repositories",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().StringVar(&projectsRoot, "projects-root", filepath.Join(home, "projects"), "root dir containing {org}/{repo}")
	root.PersistentFlags().StringVar(&filterFlag, "filter", "", "only repos whose org/name contains this substring")
	root.PersistentFlags().StringArrayVar(&extraOrgs, "org", nil, "additional GitHub owner to query (repeatable)")

	root.AddCommand(newSyncCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newCICmd())
	root.AddCommand(newHooksCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
