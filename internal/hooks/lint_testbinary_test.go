package hooks

import (
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
)

// actAsWBForHookTemplates lets this test binary stand in for $WB_EXECUTABLE
// when a built-in template runs under test: Run defaults WB_EXECUTABLE to
// os.Executable(), which is this binary. It answers exactly the two calls
// the lint templates make -- `hooks lint` and the `run --quiet --` command
// gateway -- with the real Lint implementation and a direct exec, so a
// template test exercises the real lint decisions end to end.
func actAsWBForHookTemplates(args []string) (int, bool) {
	projectsRoot := ""
	if len(args) >= 2 && args[0] == "--projects-root" {
		projectsRoot, args = args[1], args[2:]
	}
	switch {
	case len(args) >= 2 && args[0] == "hooks" && args[1] == "lint":
		flags := flag.NewFlagSet("lint", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		hook := flags.String("hook", "", "")
		language := flags.String("language", "", "")
		if err := flags.Parse(args[2:]); err != nil {
			return 2, true
		}
		executable, _ := os.Executable()
		return Lint(LintOptions{Hook: *hook, Language: *language, ProjectsRoot: projectsRoot, WBExecutable: executable}), true
	case len(args) >= 6 && args[0] == "run" && args[1] == "--quiet" && args[2] == "--timeout" && args[4] == "--":
		command := args[5:]
		if len(command) >= 2 && command[0] == "go" && command[1] == "install" {
			// Building the pinned linter needs the network; under test it is
			// always "offline", so a template test never downloads anything.
			return 1, true
		}
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), true
		}
		if err != nil {
			return 127, true
		}
		return 0, true
	}
	return 0, false
}
