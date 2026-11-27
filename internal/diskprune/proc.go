package diskprune

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Process is one entry of the process table, as far as the invoking user can
// read it. Readable is false when cwd, fd or maps could not be read: such a
// process still counts when its name identifies a tool prune must respect.
type Process struct {
	PID      int
	UID      int
	Comm     string
	Exe      string
	Argv     []string
	Environ  []string
	Cwd      string
	Files    []string // open file descriptor targets and mapped files
	Readable bool
	// Age is how long the process has run; AgeUnknown when it cannot be
	// read, which the build-cache floor treats as fail-closed.
	Age time.Duration
}

// AgeUnknown marks a process whose start time could not be read.
const AgeUnknown time.Duration = -1

// Command is the process's command line for reports, or its name when the
// command line is empty (a kernel thread or a zombie).
func (p Process) Command() string {
	if len(p.Argv) == 0 {
		return p.Comm
	}
	return strings.Join(p.Argv, " ")
}

// Evidence says whether the process table can be trusted to show every
// process that might use the caches. A table prune cannot trust makes every
// category that depends on it skip as unknown.
type Evidence struct {
	// Unsupported is true when there is no process table at all: not Linux.
	Unsupported bool
	// Unknown is non-empty when the table exists but cannot be trusted.
	Unknown string
	// HidePID is true when /proc hides other users' processes. It makes the
	// table untrustworthy for any root other users can reach.
	HidePID bool
}

// Table is one snapshot of the process table.
type Table struct {
	Processes []Process
	Evidence  Evidence
}

// procSource locates everything the reader looks at, so a test can lay out a
// fake /proc and fake container markers.
type procSource struct {
	goos      string
	procRoot  string
	rootFS    string // "/" normally; container marker files are under it
	selfPID   string // "self" normally
	now       func() time.Time
	clockTick int64
}

func realProcSource() procSource {
	return procSource{goos: runtime.GOOS, procRoot: "/proc", rootFS: "/", selfPID: "self", now: time.Now, clockTick: 100}
}

// ReadProcessTable snapshots the real process table.
func ReadProcessTable() Table { return readProcessTable(realProcSource()) }

func readProcessTable(src procSource) Table {
	if src.goos != "linux" {
		return Table{Evidence: Evidence{Unsupported: true}}
	}
	entries, err := os.ReadDir(src.procRoot)
	if err != nil {
		return Table{Evidence: Evidence{Unknown: "the process table cannot be listed: " + err.Error()}}
	}
	hidden, known := procHidesPIDs(src)
	table := Table{Evidence: Evidence{HidePID: hidden}}
	if !known {
		table.Evidence.Unknown = "the procfs mount options cannot be read, so hidden processes cannot be ruled out"
	}
	if reason := untrustedNamespace(src); reason != "" {
		table.Evidence.Unknown = reason
	}
	boot := bootTime(src)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		process, ok, err := readProcess(src, pid, boot)
		if err != nil {
			// Anything but "the process exited" (fd exhaustion, ENOMEM) makes
			// the table incomplete: a silently missing process is exactly the
			// one prune must not overlook.
			table.Evidence.Unknown = "the process table could not be read completely: " + err.Error()
			continue
		}
		if ok {
			table.Processes = append(table.Processes, process)
		}
	}
	return table
}

// readProcess reads one process. A process that exited mid-read is dropped;
// one whose private views are unreadable is kept with Readable false.
func readProcess(src procSource, pid int, boot time.Time) (Process, bool, error) {
	dir := filepath.Join(src.procRoot, strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return Process{}, false, nil
	}
	if err != nil {
		return Process{}, false, err
	}
	process := Process{PID: pid, UID: -1, Readable: true}
	process.Comm, process.Age = parseStat(string(stat), boot, src)
	process.UID = readUID(filepath.Join(dir, "status"))
	raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if untrustedRead(err) {
		return Process{}, false, err
	}
	process.Argv = splitNUL(raw)
	// environ is private to the process's user; permission is expected, any
	// other failure (EMFILE) would silently lose an env-only reference.
	raw, err = os.ReadFile(filepath.Join(dir, "environ"))
	if untrustedRead(err) {
		return Process{}, false, err
	}
	process.Environ = splitNUL(raw)
	process.Exe, _ = os.Readlink(filepath.Join(dir, "exe"))
	cwd, cwdErr := os.Readlink(filepath.Join(dir, "cwd"))
	process.Cwd = cwd
	fds, fdErr := os.ReadDir(filepath.Join(dir, "fd"))
	unreadableFD := false
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
		if untrustedRead(err) {
			return Process{}, false, err
		}
		// A descriptor that cannot be read (as opposed to one that closed)
		// may be the very reference prune is looking for.
		if errors.Is(err, fs.ErrPermission) {
			unreadableFD = true
		}
		if strings.HasPrefix(target, "/") {
			process.Files = append(process.Files, target)
		}
	}
	maps, mapsErr := os.ReadFile(filepath.Join(dir, "maps"))
	process.Files = append(process.Files, mappedFiles(string(maps))...)
	process.Readable = cwdErr == nil && fdErr == nil && mapsErr == nil && !unreadableFD
	return process, true, nil
}

// parseStat returns the command name and the process's age. The name is in
// parentheses and may itself contain spaces and parentheses, so fields are
// counted from the last closing parenthesis.
func parseStat(stat string, boot time.Time, src procSource) (string, time.Duration) {
	open := strings.IndexByte(stat, '(')
	closing := strings.LastIndexByte(stat, ')')
	if open < 0 || closing < open {
		return "", AgeUnknown
	}
	comm := stat[open+1 : closing]
	fields := strings.Fields(stat[closing+1:])
	// starttime is field 22 overall; after the name, field 3 is index 0.
	if len(fields) < 20 || boot.IsZero() {
		return comm, AgeUnknown
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return comm, AgeUnknown
	}
	started := boot.Add(time.Duration(ticks) * time.Second / time.Duration(src.clockTick))
	return comm, src.now().Sub(started)
}

func bootTime(src procSource) time.Time {
	raw, err := os.ReadFile(filepath.Join(src.procRoot, "stat"))
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				return time.Unix(seconds, 0)
			}
		}
	}
	return time.Time{}
}

func readUID(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "Uid:"); ok {
			fields := strings.Fields(value)
			if len(fields) > 0 {
				if uid, err := strconv.Atoi(fields[0]); err == nil {
					return uid
				}
			}
		}
	}
	return -1
}

func splitNUL(raw []byte) []string {
	var out []string
	for _, part := range strings.Split(string(raw), "\x00") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// mappedFiles returns the file paths in a maps listing. The pathname is
// everything after the five fixed fields and their padding, byte for byte,
// so a path with consecutive spaces is kept exactly; the kernel escapes only
// a newline in it, as \012.
func mappedFiles(maps string) []string {
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(maps, "\n") {
		path, ok := mapsPathname(line)
		if !ok {
			continue
		}
		path = strings.ReplaceAll(strings.TrimSuffix(path, " (deleted)"), `\012`, "\n")
		if !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	return files
}

// mapsPathname is a maps line's pathname column, when it is an absolute
// path.
func mapsPathname(line string) (string, bool) {
	rest := line
	for field := 0; field < 5; field++ {
		rest = strings.TrimLeft(rest, " ")
		end := strings.IndexByte(rest, ' ')
		if end < 0 {
			return "", false
		}
		rest = rest[end:]
	}
	rest = strings.TrimLeft(rest, " ")
	return rest, strings.HasPrefix(rest, "/")
}

// procHidesPIDs reports whether the procfs at procRoot is mounted with a
// hidepid option other than 0, which hides other users' processes, and
// whether that could be determined at all.
func procHidesPIDs(src procSource) (hidden, known bool) {
	raw, err := os.ReadFile(filepath.Join(src.procRoot, src.selfPID, "mountinfo"))
	if err != nil {
		return false, false
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	sawProc := false
	for scanner.Scan() {
		line := scanner.Text()
		left, right, ok := strings.Cut(line, " - ")
		fields := strings.Fields(left)
		if !ok || len(fields) < 5 || fields[4] != "/proc" {
			continue
		}
		options := strings.Fields(right)
		if len(options) < 3 || options[0] != "proc" {
			continue
		}
		sawProc = true
		for _, option := range strings.Split(options[2], ",") {
			if value, ok := strings.CutPrefix(option, "hidepid="); ok && value != "0" && value != "off" {
				return true, true
			}
		}
	}
	// Without a complete listing that positively shows the procfs mount,
	// hidden processes cannot be ruled out.
	return false, scanner.Err() == nil && sawProc
}

// untrustedNamespace names why the visible process table may omit processes
// that share the caches: a nested PID namespace, or a container whose own
// procfs shows a single namespace while host processes use the same files.
func untrustedNamespace(src procSource) string {
	if raw, err := os.ReadFile(filepath.Join(src.procRoot, src.selfPID, "status")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if value, ok := strings.CutPrefix(line, "NSpid:"); ok && len(strings.Fields(value)) > 1 {
				return "prune runs in a nested PID namespace, so processes outside it are invisible"
			}
		}
	}
	for _, marker := range []string{".dockerenv", filepath.Join("run", ".containerenv"), filepath.Join("run", "systemd", "container")} {
		if _, err := os.Stat(filepath.Join(src.rootFS, marker)); err == nil {
			return "prune runs in a container (" + marker + "), whose process table may omit host processes sharing its caches"
		}
	}
	if raw, err := os.ReadFile(filepath.Join(src.procRoot, src.selfPID, "cgroup")); err == nil {
		for _, runtime := range []string{"docker", "kubepods", "containerd", "libpod", "lxc"} {
			if strings.Contains(string(raw), runtime) {
				return "prune runs in a container (cgroup names " + runtime + "), whose process table may omit host processes sharing its caches"
			}
		}
	}
	// PID 1's environment is root-only on most hosts; when it is readable it
	// names the container runtime.
	if raw, err := os.ReadFile(filepath.Join(src.procRoot, "1", "environ")); err == nil {
		for _, variable := range splitNUL(raw) {
			if strings.HasPrefix(variable, "container=") {
				return "prune runs in a container (" + variable + "), whose process table may omit host processes sharing its caches"
			}
		}
	}
	return ""
}

// goBuildTools set the build-cache floor and make Go-produced scratch and the
// module cache busy.
var goBuildTools = map[string]bool{
	"go": true, "compile": true, "link": true, "asm": true, "cgo": true, "vet": true,
	"golangci-lint": true, "staticcheck": true, "govulncheck": true,
}

// goLongLivedTools read the module cache but can run for weeks, so they never
// set the build-cache floor.
var goLongLivedTools = map[string]bool{"gopls": true, "dlv": true}

func (p Process) names() []string {
	var names []string
	if p.Exe != "" {
		names = append(names, filepath.Base(strings.TrimSuffix(p.Exe, " (deleted)")))
	}
	if len(p.Argv) > 0 {
		names = append(names, filepath.Base(p.Argv[0]))
	}
	if p.Comm != "" {
		names = append(names, p.Comm)
	}
	return names
}

// IsGoBuildTool reports a compiler, linker, test binary or linter.
func (p Process) IsGoBuildTool() bool {
	for _, name := range p.names() {
		if goBuildTools[name] || strings.HasSuffix(name, ".test") {
			return true
		}
	}
	return false
}

// IsGoTool reports any Go tool, including the long-lived ones.
func (p Process) IsGoTool() bool {
	if p.IsGoBuildTool() {
		return true
	}
	for _, name := range p.names() {
		if goLongLivedTools[name] {
			return true
		}
	}
	return false
}

// IsPnpm reports pnpm itself, or node running pnpm's entry script: pnpm
// usually runs as node, so its process name alone would miss it.
func (p Process) IsPnpm() bool {
	names := p.names()
	for _, name := range names {
		if name == "pnpm" {
			return true
		}
	}
	isNode := false
	for _, name := range names {
		if name == "node" || name == "MainThread" {
			isNode = true
		}
	}
	if !isNode {
		return false
	}
	for _, arg := range p.Argv[min(1, len(p.Argv)):] {
		if strings.HasPrefix(filepath.Base(arg), "pnpm") {
			return true
		}
	}
	return false
}

// References reports whether the process uses a location inside path through
// its working directory, an open or mapped file, its command line or its
// environment.
func (p Process) References(path string) bool {
	path = filepath.Clean(path)
	if within(p.Cwd, path) {
		return true
	}
	for _, file := range p.Files {
		if within(file, path) {
			return true
		}
	}
	for _, text := range append(append([]string{}, p.Argv...), p.Environ...) {
		if mentionsPath(text, path) {
			return true
		}
	}
	// An absolute argument is compared cleaned too (/tmp/./x is /tmp/x); a
	// relative one names a path below the working directory: "worker
	// go-build77/input" run from /tmp names /tmp/go-build77/input.
	for _, arg := range p.Argv {
		switch {
		case filepath.IsAbs(arg) && within(filepath.Clean(arg), path):
			return true
		case p.Cwd != "" && arg != "" && !filepath.IsAbs(arg) && !strings.HasPrefix(arg, "-") && within(filepath.Join(p.Cwd, arg), path):
			return true
		}
	}
	return false
}

// within reports whether candidate is path or lies below it.
func within(candidate, path string) bool {
	if candidate == "" {
		return false
	}
	candidate = filepath.Clean(strings.TrimSuffix(candidate, " (deleted)"))
	return candidate == path || strings.HasPrefix(candidate, path+string(filepath.Separator))
}

// mentionsPath finds path inside free text such as an argument or KEY=VALUE,
// only where it stands as a whole path: preceded by the start, '=', ':', a
// space or a quote, and followed by the end, a separator, ':', a space or a
// quote.
func mentionsPath(text, path string) bool {
	for offset := 0; ; {
		index := strings.Index(text[offset:], path)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(path)
		before := start == 0 || strings.ContainsRune("=: \"',;@", rune(text[start-1])) ||
			// a joined flag such as -I/tmp/go-build1/b001
			(start >= 2 && text[start-2] == '-' && isLetter(text[start-1]))
		after := end == len(text) || strings.ContainsRune("/: \"',;", rune(text[end]))
		if before && after {
			return true
		}
		offset = start + 1
	}
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// untrustedRead is a read failure that is neither "not permitted" nor "the
// process or descriptor is gone": the table can no longer be complete.
func untrustedRead(err error) bool {
	return err != nil && !errors.Is(err, fs.ErrPermission) && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ESRCH)
}
