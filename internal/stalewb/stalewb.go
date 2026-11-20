// Package stalewb finds copies of wb on PATH that are older than the one the
// user just installed or is running. A package manager, `go install` and a
// hand-copied binary each own a different directory, so an update through one
// leaves the others behind, and any shell whose PATH is ordered differently
// runs the stale one. The package only reports: it never deletes or
// overwrites a copy.
package stalewb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"golang.org/x/mod/semver"
)

// SkipEnv is set on every probe child. A probed wb that itself scans PATH
// would spawn the same probes again and never stop.
const SkipEnv = "WB_SKIP_STALE_SCAN"

// ProbeTimeout bounds one `version --json` probe.
const ProbeTimeout = 3 * time.Second

// Binary is one stale wb on PATH.
type Binary struct {
	// Path is the PATH entry, the file `rm` removes.
	Path string `json:"path"`
	// RealPath is Path with symlinks resolved; it differs from Path for a
	// launcher symlink.
	RealPath string `json:"real_path,omitempty"`
	Version  string `json:"version"`
}

// Scanner finds and probes wb executables. Probe is the seam: it returns the
// version a candidate reports, and production runs the candidate while tests
// return canned versions.
type Scanner struct {
	// PathEnv is the PATH value to search.
	PathEnv string
	// Probe returns the version the executable at path reports.
	Probe func(ctx context.Context, path string) (string, error)
	// Skip names an executable, by real path, that is not to be probed: the
	// running binary.
	Skip string
	// Executable decides from a file's info whether it can be run; nil uses
	// the platform rule (an execute bit, or any regular file on Windows).
	Executable func(os.FileInfo) bool
}

func executableFile(info os.FileInfo) bool { return isExecutable(info.Mode(), runtime.GOOS) }

// isExecutable reports whether a regular file's mode is runnable: Windows has
// no execute bit, everywhere else one must be set.
func isExecutable(mode os.FileMode, goos string) bool {
	return mode.IsRegular() && (goos == "windows" || mode.Perm()&0o111 != 0)
}

// New returns the scanner production uses: the process PATH, a probe that
// runs `version --json`, and the running executable skipped.
func New() Scanner {
	skip, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(skip); err == nil {
		skip = resolved
	}
	return Scanner{PathEnv: os.Getenv("PATH"), Probe: ProbeWith(runner.New()), Skip: skip}
}

// Scan returns the wb copies on PATH older than current. A process that is
// itself a probe child (SkipEnv set) scans nothing, so probes never recurse.
func Scan(ctx context.Context, current string) []Binary {
	return scanUnless(os.Getenv(SkipEnv), New, ctx, current)
}

func scanUnless(marker string, scanner func() Scanner, ctx context.Context, current string) []Binary {
	if marker != "" {
		return nil
	}
	return scanner().Stale(ctx, current)
}

// ProbeWith returns a probe that runs `path version --json` through run with a
// bounded timeout and the probe marker in the child's environment, and returns
// the version it reports. Only Find hands it a regular executable file named
// wb.
func ProbeWith(run runner.Runner) func(context.Context, string) (string, error) {
	return func(ctx context.Context, path string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
		defer cancel()
		result, err := run.RunOpts(ctx, "", runner.RunOptions{
			Env:           append(os.Environ(), SkipEnv+"=1"),
			StdoutLimit:   1 << 16,
			DiscardStderr: true,
		}, path, "version", "--json")
		if err != nil {
			return "", err
		}
		return ParseVersion([]byte(result.Stdout))
	}
}

// ParseVersion reads the `version` key of a `version --json` document.
func ParseVersion(output []byte) (string, error) {
	var report struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		return "", err
	}
	if report.Version == "" {
		return "", errors.New("no version in output")
	}
	return report.Version, nil
}

// Candidate is a wb executable found on PATH.
type Candidate struct{ Path, Real string }

// Find lists the wb executables on PATH in PATH order, once per real file. A
// candidate is a PATH entry named wb whose target resolves to a regular file
// with an execute bit; a directory, a device, a dangling symlink or a
// non-executable file is never probed.
func (scanner Scanner) Find() []Candidate {
	executable := scanner.Executable
	if executable == nil {
		executable = executableFile
	}
	seen := map[string]bool{}
	var found []Candidate
	for _, directory := range filepath.SplitList(scanner.PathEnv) {
		if directory == "" {
			continue
		}
		for _, name := range []string{"wb", "wb.exe"} {
			path := filepath.Join(directory, name)
			real, err := filepath.EvalSymlinks(path)
			if err != nil || seen[real] {
				continue
			}
			info, err := os.Stat(real)
			if err != nil || !info.Mode().IsRegular() || !executable(info) {
				continue
			}
			seen[real] = true
			found = append(found, Candidate{Path: path, Real: real})
		}
	}
	return found
}

// Stale probes every wb on PATH and returns those that report a version older
// than current. A candidate whose version cannot be read, or that is not a
// semantic version, is not reported: it is not shown to be older.
func (scanner Scanner) Stale(ctx context.Context, current string) []Binary {
	if ctx == nil {
		ctx = context.Background()
	}
	want := canonical(current)
	if !semver.IsValid(want) {
		return nil
	}
	var stale []Binary
	for _, found := range scanner.Find() {
		if found.Real == scanner.Skip {
			continue
		}
		reported, err := scanner.Probe(ctx, found.Path)
		if err != nil {
			continue
		}
		got := canonical(reported)
		if !semver.IsValid(got) || semver.Compare(got, want) >= 0 {
			continue
		}
		binary := Binary{Path: found.Path, Version: strings.TrimPrefix(got, "v")}
		if found.Real != found.Path {
			binary.RealPath = found.Real
		}
		stale = append(stale, binary)
	}
	return stale
}

func canonical(version string) string {
	version = strings.TrimSpace(version)
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version
}

// Warning renders the stale copies and the exact fix for each. It never
// removes anything. An empty list renders "".
func Warning(current string, stale []Binary) string {
	if len(stale) == 0 {
		return ""
	}
	var text strings.Builder
	fmt.Fprintf(&text, "warning: older wb copies on PATH than %s; a shell that finds one first runs it:\n", strings.TrimPrefix(current, "v"))
	for _, binary := range stale {
		fmt.Fprintf(&text, "  %s is %s", binary.Path, binary.Version)
		if binary.RealPath != "" {
			fmt.Fprintf(&text, " (symlink to %s)", binary.RealPath)
		}
		fmt.Fprintf(&text, "\n    fix: rm %s   (or re-run the install method that owns it)\n", shellQuote(binary.Path))
	}
	return text.String()
}

func shellQuote(path string) string {
	if !strings.ContainsAny(path, " \t'\"\\$`") {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}
