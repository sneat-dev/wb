package cmdversion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

func execute(t *testing.T, info buildinfo.Report, args ...string) (string, int, error) {
	t.Helper()
	calls := 0
	command := New(func() buildinfo.Report { calls++; return info })
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(io.Discard)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), calls, err
}

func report() buildinfo.Report {
	return buildinfo.Report{Version: "unknown", JSONVersion: "dev", Revision: "abc", Built: "today", Modified: true, Go: "go-test", Platform: "os/arch", Name: "wb", Commit: "abc+dirty", Date: "today", DateSource: "stamp"}
}

func TestCommandFormatsAndIsolation(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"--json"}, {"--format=json"}, {"--json", "--format=text"}, {"--format=json", "--json=false"}, {"--format=text", "--json"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			t.Parallel()
			out, calls, err := execute(t, report(), args...)
			if err != nil || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			asJSON := len(args) > 0 && (args[len(args)-1] == "--json" || args[len(args)-1] == "--format=json")
			if asJSON {
				want := "{\n  \"version\": \"dev\",\n  \"revision\": \"abc\",\n  \"built\": \"today\",\n  \"modified\": true,\n  \"go\": \"go-test\",\n  \"platform\": \"os/arch\",\n  \"name\": \"wb\",\n  \"commit\": \"abc+dirty\",\n  \"date\": \"today\",\n  \"date_source\": \"stamp\"\n}\n"
				if out != want {
					t.Fatalf("JSON output=%s, want=%s", out, want)
				}
			} else if want := "wb unknown\nrevision: abc (modified)\nbuilt:    today\ngo:       go-test\nplatform: os/arch\n"; out != want {
				t.Fatalf("text=%q, want=%q", out, want)
			}
		})
	}
}

// The fleet contract keys remain present even when optional WB metadata is absent.
func TestVersionJSON_ContractKeys(t *testing.T) {
	t.Parallel()
	out, _, err := execute(t, buildinfo.Report{Version: "1.2.3", JSONVersion: "1.2.3", Name: "wb"}, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "version", "commit", "date", "date_source"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing contract key %q: %s", key, out)
		}
	}
	for _, key := range []string{"revision", "built", "modified", "JSONVersion", "json_version"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("unexpected key %q: %s", key, out)
		}
	}
	var name, version string
	_ = json.Unmarshal(decoded["name"], &name)
	_ = json.Unmarshal(decoded["version"], &version)
	if name != "wb" {
		t.Errorf("name=%q, want wb", name)
	}
	if version != "1.2.3" {
		t.Errorf("version=%q, want 1.2.3 unchanged", version)
	}
}

func TestInvalidArgumentsNeverCollectSnapshot(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"extra"}, {"--format=xml"}, {"--unknown"}} {
		_, calls, err := execute(t, report(), args...)
		if err == nil || calls != 0 {
			t.Fatalf("args=%q error=%v calls=%d", args, err, calls)
		}
	}
}

type failAfter struct {
	remaining int
	err       error
}

func (w *failAfter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, w.err
	}
	w.remaining--
	return len(p), nil
}

func TestWriterFailuresPropagateUnchanged(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("output closed")
	for n := 0; n < 5; n++ {
		if err := Write(&failAfter{remaining: n, err: sentinel}, report(), false); !errors.Is(err, sentinel) {
			t.Fatalf("text write %d: %v", n, err)
		}
	}
	for _, args := range [][]string{nil, {"--json"}} {
		command := New(report)
		command.SetOut(&failAfter{err: sentinel})
		command.SetArgs(args)
		if err := command.Execute(); !errors.Is(err, sentinel) {
			t.Fatalf("command %q: %v", args, err)
		}
	}
	if err := WriteBare(&failAfter{err: sentinel}, "unknown"); !errors.Is(err, sentinel) {
		t.Fatalf("bare: %v", err)
	}
}

func TestTextOptionalFieldsAndBare(t *testing.T) {
	t.Parallel()
	info := report()
	info.Modified = false
	info.Built = ""
	var out bytes.Buffer
	if err := Write(&out, info, false); err != nil {
		t.Fatal(err)
	}
	if want := "wb unknown\nrevision: abc\ngo:       go-test\nplatform: os/arch\n"; out.String() != want {
		t.Fatalf("output=%q want=%q", out.String(), want)
	}
	out.Reset()
	info.Revision = ""
	if err := Write(&out, info, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "revision:") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := WriteBare(&out, info.Version); err != nil {
		t.Fatal(err)
	}
	if out.String() != "unknown\n" {
		t.Fatal(out.String())
	}
}
