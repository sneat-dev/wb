package diskprune

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeProc lays out a /proc tree. Symlinks stand in for exe, cwd and fd
// entries; leaving one out makes it unreadable, as for another user's or a
// non-dumpable process.
type fakeProc struct {
	t    *testing.T
	root string
	fs   string
	now  time.Time
}

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	p := &fakeProc{t: t, root: filepath.Join(t.TempDir(), "proc"), fs: t.TempDir(), now: time.Unix(1_000_000, 0)}
	p.write("stat", "cpu 1 2 3\nbtime 990000\n")
	p.write("self/status", "Name:\twb\nNSpid:\t42\n")
	p.write("self/mountinfo", "22 1 0:21 / /proc rw,nosuid - proc proc rw\n")
	return p
}

func (p *fakeProc) write(rel, content string) {
	p.t.Helper()
	path := filepath.Join(p.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *fakeProc) link(rel, target string) {
	p.t.Helper()
	path := filepath.Join(p.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		p.t.Fatal(err)
	}
}

type fakeProcess struct {
	pid     int
	comm    string
	argv    []string
	environ []string
	exe     string
	cwd     string
	fds     []string
	maps    []string
	uid     int
	started time.Duration // seconds after boot, in clock ticks of 100 Hz
	opaque  bool          // cwd, fd and maps unreadable
}

func (p *fakeProc) add(process fakeProcess) {
	p.t.Helper()
	dir := strconv.Itoa(process.pid)
	ticks := int64(process.started / (10 * time.Millisecond))
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[19] = strconv.FormatInt(ticks, 10)
	p.write(dir+"/stat", strconv.Itoa(process.pid)+" ("+process.comm+") "+strings.Join(fields, " "))
	p.write(dir+"/status", "Name:\t"+process.comm+"\nUid:\t"+strconv.Itoa(process.uid)+"\t0\t0\t0\n")
	p.write(dir+"/cmdline", strings.Join(process.argv, "\x00")+"\x00")
	p.write(dir+"/environ", strings.Join(process.environ, "\x00"))
	if process.exe != "" {
		p.link(dir+"/exe", process.exe)
	}
	if process.opaque {
		return
	}
	p.link(dir+"/cwd", process.cwd)
	if err := os.MkdirAll(filepath.Join(p.root, dir, "fd"), 0o755); err != nil {
		p.t.Fatal(err)
	}
	for i, fd := range process.fds {
		p.link(dir+"/fd/"+strconv.Itoa(i), fd)
	}
	var maps strings.Builder
	for _, path := range process.maps {
		maps.WriteString("7f00-7f01 r--p 00000000 08:01 123 " + path + "\n")
	}
	maps.WriteString("7f02-7f03 rw-p 00000000 00:00 0 [heap]\n")
	p.write(dir+"/maps", maps.String())
}

func (p *fakeProc) source() procSource {
	return procSource{goos: "linux", procRoot: p.root, rootFS: p.fs, selfPID: "self", now: func() time.Time { return p.now }, clockTick: 100}
}

func processByPID(t *testing.T, table Table, pid int) Process {
	t.Helper()
	for _, process := range table.Processes {
		if process.PID == pid {
			return process
		}
	}
	t.Fatalf("pid %d not in table", pid)
	return Process{}
}

// Every view prune relies on is read: name, command line, environment,
// executable, working directory, open and mapped files, owner and age.
func TestProcessTableReadsEveryView(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(t)
	proc.add(fakeProcess{
		pid: 4242, comm: "compile", argv: []string{"/usr/local/go/pkg/tool/linux_amd64/compile", "-o", "/tmp/go-build1/b001/_pkg_.a"},
		environ: []string{"GOCACHE=/home/u/.cache/go-build"}, exe: "/usr/local/go/pkg/tool/linux_amd64/compile",
		cwd: "/home/u/projects/acme/app", fds: []string{"/tmp/go-build1/b001/importcfg", "pipe:[1]"},
		maps: []string{"/usr/lib/libc.so.6", "/tmp/go-build2/x (deleted)"}, uid: 1001, started: 9000 * time.Second,
	})
	proc.write("self/notapid", "")
	proc.write("0/stat", "")
	table := readProcessTable(proc.source())
	if table.Evidence != (Evidence{}) {
		t.Fatalf("evidence = %#v, want trusted", table.Evidence)
	}
	process := processByPID(t, table, 4242)
	if process.Comm != "compile" || process.UID != 1001 || !process.Readable || process.Age != time.Second*(1_000_000-990_000-9000) {
		t.Fatalf("process = %#v", process)
	}
	if !process.IsGoBuildTool() || !process.IsGoTool() || process.IsPnpm() {
		t.Fatal("compile misclassified")
	}
	for _, path := range []string{"/tmp/go-build1", "/home/u/.cache/go-build", "/home/u/projects/acme", "/tmp/go-build2"} {
		if !process.References(path) {
			t.Errorf("compile does not reference %s", path)
		}
	}
	for _, path := range []string{"/tmp/go-build", "/home/u/.cache/go", "/tmp/other"} {
		if process.References(path) {
			t.Errorf("compile wrongly references %s", path)
		}
	}
	if !strings.HasPrefix(process.Command(), "/usr/local/go/pkg/tool/linux_amd64/compile -o") {
		t.Fatalf("command = %q", process.Command())
	}
}

// A process whose private views cannot be read stays in the table, marked
// unreadable, so a Go tool or pnpm can still be counted by name.
func TestProcessTableKeepsUnreadableProcesses(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(t)
	proc.add(fakeProcess{pid: 7, comm: "sshd", argv: []string{"sshd: u@pts/0"}, opaque: true, uid: 1001})
	proc.add(fakeProcess{pid: 8, comm: "go", argv: []string{"go", "build", "./..."}, opaque: true, uid: 0})
	proc.add(fakeProcess{pid: 9, comm: "kworker/0:1", uid: 0, opaque: true})
	table := readProcessTable(proc.source())
	sshd := processByPID(t, table, 7)
	if sshd.Readable || sshd.IsGoTool() || sshd.IsPnpm() {
		t.Fatalf("sshd = %#v", sshd)
	}
	if goProcess := processByPID(t, table, 8); goProcess.Readable || !goProcess.IsGoBuildTool() {
		t.Fatalf("go = %#v", goProcess)
	}
	if kernel := processByPID(t, table, 9); kernel.Command() != "kworker/0:1" {
		t.Fatalf("kernel thread command = %q", kernel.Command())
	}
}

// pnpm usually runs as node; long-lived Go tools are Go tools but never build
// tools; a test binary is a build tool by its suffix.
func TestProcessClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		process       Process
		build, gotool bool
		pnpm          bool
	}{
		{Process{Argv: []string{"node", "/usr/lib/node_modules/pnpm/bin/pnpm.cjs", "install"}, Comm: "node"}, false, false, true},
		{Process{Argv: []string{"/usr/bin/pnpm", "store", "prune"}}, false, false, true},
		{Process{Argv: []string{"node", "server.js"}, Comm: "MainThread"}, false, false, false},
		{Process{Argv: []string{"node"}, Comm: "node"}, false, false, false},
		{Process{Exe: "/home/u/go/bin/gopls (deleted)"}, false, true, false},
		{Process{Argv: []string{"/tmp/go-build9/b001/disk.test", "-test.v"}}, true, true, false},
		{Process{Comm: "golangci-lint"}, true, true, false},
		{Process{Comm: "bash"}, false, false, false},
	}
	for _, c := range cases {
		if c.process.IsGoBuildTool() != c.build || c.process.IsGoTool() != c.gotool || c.process.IsPnpm() != c.pnpm {
			t.Errorf("%#v: build %t tool %t pnpm %t", c.process, c.process.IsGoBuildTool(), c.process.IsGoTool(), c.process.IsPnpm())
		}
	}
}

// Paths are matched as whole paths inside free text, never as prefixes of a
// longer name.
func TestMentionsPathMatchesWholePathsOnly(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]bool{
		"GOCACHE=/tmp/wb-a-cache":          true,
		"PATH=/bin:/tmp/wb-a-cache/bin:/x": true,
		"--dir '/tmp/wb-a-cache'":          true,
		"/tmp/wb-a-cache":                  true,
		"/tmp/wb-a-cache-2":                false,
		"x/tmp/wb-a-cache":                 false,
		"/tmp/wb-a-cachex /tmp/wb-a-cache": true,
	} {
		if got := mentionsPath(text, "/tmp/wb-a-cache"); got != want {
			t.Errorf("mentionsPath(%q) = %t, want %t", text, got, want)
		}
	}
	if within("", "/tmp") {
		t.Fatal("an empty path is within nothing")
	}
}

// Evidence prune cannot trust: no process table, an unlistable one, hidden
// PIDs, a nested namespace, or a container.
func TestProcessTableEvidence(t *testing.T) {
	t.Parallel()
	if table := readProcessTable(procSource{goos: "darwin"}); !table.Evidence.Unsupported {
		t.Fatal("non-Linux must be unsupported")
	}
	missing := newFakeProc(t)
	missing.root = filepath.Join(missing.root, "absent")
	if table := readProcessTable(missing.source()); !strings.Contains(table.Evidence.Unknown, "cannot be listed") {
		t.Fatalf("unlistable: %#v", table.Evidence)
	}

	hidden := newFakeProc(t)
	hidden.write("self/mountinfo", "21 1 0:20 / /sys rw - sysfs sysfs rw\nshort line\n22 1 0:21 / /proc rw - proc proc rw,hidepid=invisible\n")
	if table := readProcessTable(hidden.source()); !table.Evidence.HidePID {
		t.Fatal("hidepid=invisible not detected")
	}
	visible := newFakeProc(t)
	visible.write("self/mountinfo", "22 1 0:21 / /proc rw - proc proc rw,hidepid=0\n23 1 0:22 / /proc rw - tmpfs t rw\n24 1 0:23 / /proc rw - proc\n")
	if table := readProcessTable(visible.source()); table.Evidence.HidePID {
		t.Fatal("hidepid=0 reported as hidden")
	}

	nested := newFakeProc(t)
	nested.write("self/status", "NSpid:\t42\t7\n")
	if table := readProcessTable(nested.source()); !strings.Contains(table.Evidence.Unknown, "nested PID namespace") {
		t.Fatalf("nested: %#v", table.Evidence)
	}
	docker := newFakeProc(t)
	if err := os.WriteFile(filepath.Join(docker.fs, ".dockerenv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if table := readProcessTable(docker.source()); !strings.Contains(table.Evidence.Unknown, ".dockerenv") {
		t.Fatalf("docker: %#v", table.Evidence)
	}
	podman := newFakeProc(t)
	podman.add(fakeProcess{pid: 1, comm: "init", environ: []string{"HOME=/", "container=podman"}, opaque: true})
	if table := readProcessTable(podman.source()); !strings.Contains(table.Evidence.Unknown, "container=podman") {
		t.Fatalf("podman: %#v", table.Evidence)
	}
}

// Malformed or missing stat details degrade to an unknown age, never a crash.
func TestProcessStatParsingTolerance(t *testing.T) {
	t.Parallel()
	src := procSource{now: time.Now, clockTick: 100}
	if comm, age := parseStat("garbage", time.Unix(1, 0), src); comm != "" || age != 0 {
		t.Fatalf("garbage = %q %v", comm, age)
	}
	if comm, age := parseStat("1 (a (b)) S 1", time.Unix(1, 0), src); comm != "a (b)" || age != 0 {
		t.Fatalf("short = %q %v", comm, age)
	}
	fields := strings.Repeat("0 ", 19) + "notanumber"
	if _, age := parseStat("1 (x) "+fields, time.Unix(1, 0), src); age != 0 {
		t.Fatalf("bad starttime age = %v", age)
	}
	if _, age := parseStat("1 (x) "+strings.Repeat("0 ", 20), time.Time{}, src); age != 0 {
		t.Fatal("unknown boot time must give no age")
	}

	proc := newFakeProc(t)
	proc.write("stat", "btime notanumber\n")
	if !bootTime(proc.source()).IsZero() {
		t.Fatal("bad btime parsed")
	}
	proc.write("1/stat", "1 (x) S")
	proc.write("1/status", "Uid:\tnotanumber\n")
	proc.write("2/stat", "2 (y) S")
	proc.write("2/status", "Uid:\n")
	table := readProcessTable(proc.source())
	if processByPID(t, table, 1).UID != -1 || processByPID(t, table, 2).UID != -1 {
		t.Fatal("unparsable uid must be -1")
	}
	if readUID(filepath.Join(proc.root, "absent")) != -1 {
		t.Fatal("missing status must give uid -1")
	}
	noBoot := newFakeProc(t)
	if err := os.Remove(filepath.Join(noBoot.root, "stat")); err != nil {
		t.Fatal(err)
	}
	if !bootTime(noBoot.source()).IsZero() {
		t.Fatal("missing /proc/stat must give no boot time")
	}
	if table := ReadProcessTable(); len(table.Processes) == 0 && !table.Evidence.Unsupported && table.Evidence.Unknown == "" {
		t.Fatal("the real process table is empty")
	}
}

// A process that exits between listing and reading is dropped, and a missing
// mountinfo means no hidepid evidence rather than an error.
func TestProcessTableToleratesVanishingEntries(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(t)
	proc.write("31/cmdline", "gone\x00")
	if err := os.Remove(filepath.Join(proc.root, "self", "mountinfo")); err != nil {
		t.Fatal(err)
	}
	table := readProcessTable(proc.source())
	if len(table.Processes) != 0 || table.Evidence.HidePID {
		t.Fatalf("table = %#v", table)
	}
}
