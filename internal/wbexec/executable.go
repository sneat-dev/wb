package wbexec

import (
	"os"
	"os/exec"
)

func hookExecutableWith(executable func() (string, error), lookPath func(string) (string, error)) string {
	if path, err := executable(); err == nil {
		return path
	}
	if path, err := lookPath("wb"); err == nil {
		return path
	}
	return "wb"
}

// resolveWBExecutableForHook decides what a governed-command rewrite should
// splice in front of "run --": self (the hook's own executable, as
// hookExecutable() resolves it) when PATH would find a DIFFERENT binary
// under the name "wb", and "" — meaning "use the bare name 'wb', unquoted" —
// when lookPath("wb") resolves to the identical file self already is.
//
// This exists because the bare name is what a user's own Claude Code
// permission rule is written against (`Bash(wb run:*)`): a rewrite that
// always spliced in the absolute path stopped matching that rule the moment
// the two files were the same binary anyway (wb#645 review r2, NM1/minor 2).
// os.SameFile compares device and inode, not string equality, so a
// symlink, a different relative spelling, or hash-cached shell lookup that
// still ultimately names this same executable is still treated as a match.
// When the two are genuinely different files — self was launched from a
// build directory or an absolute path never added to PATH, while some other
// "wb" (or none) sits on PATH — only the absolute path is guaranteed to run
// the same guard that is making this decision, so it is returned for the
// caller to shell-quote before splicing.
func resolveGovernorWith(self string, lookPath func(string) (string, error), stat func(string) (os.FileInfo, error)) string {
	if self == "" {
		return self
	}
	onPath, err := lookPath("wb")
	if err != nil {
		return self
	}
	selfInfo, err := stat(self)
	if err != nil {
		return self
	}
	onPathInfo, err := stat(onPath)
	if err != nil {
		return self
	}
	if os.SameFile(selfInfo, onPathInfo) {
		return ""
	}
	return self
}

// HookExecutable resolves the current WB process identity, with the existing PATH fallback.
func HookExecutable() string { return hookExecutableWith(os.Executable, exec.LookPath) }

// ResolveGovernorExecutable preserves a bare wb permission rule only for the same installed file.
func ResolveGovernorExecutable(self string) string {
	return resolveGovernorWith(self, exec.LookPath, os.Stat)
}
