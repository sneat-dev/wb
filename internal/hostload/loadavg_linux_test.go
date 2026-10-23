//go:build linux

package hostload

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReadLoadAvg1FromPathWrapsMissingFileError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing-loadavg")

	load, err := readLoadAvg1FromPath(path)
	if load != 0 || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("readLoadAvg1FromPath = (%v, %v), want zero load and missing-file error", load, err)
	}
	if !strings.Contains(err.Error(), "read "+path+":") {
		t.Fatalf("error %q does not name the file read", err)
	}
}

func TestReadLoadAvg1FromPathRejectsEmptyInput(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", " \t\n"} {
		path := filepath.Join(t.TempDir(), "loadavg")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}

		load, err := readLoadAvg1FromPath(path)
		if load != 0 || err == nil {
			t.Fatalf("readLoadAvg1FromPath(%q) = (%v, %v), want zero load and parse error", raw, load, err)
		}
		if want := "parse " + path + " " + strconv.Quote(raw); err.Error() != want {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
}

func TestReadLoadAvg1FromPathWrapsInvalidNumberError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "loadavg")
	const raw = "invalid 2.10 1.98\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	load, err := readLoadAvg1FromPath(path)
	var parseErr *strconv.NumError
	if load != 0 || !errors.As(err, &parseErr) {
		t.Fatalf("readLoadAvg1FromPath = (%v, %v), want zero load and wrapped numeric error", load, err)
	}
	if parseErr.Num != "invalid" || !errors.Is(err, strconv.ErrSyntax) {
		t.Fatalf("numeric error = %#v, want syntax error for the first field", parseErr)
	}
	if !strings.Contains(err.Error(), "parse "+path+" "+strconv.Quote(raw)+":") {
		t.Fatalf("error %q does not name the file and its contents", err)
	}
}

func TestReadLoadAvg1FromPathReadsFirstField(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "loadavg")
	if err := os.WriteFile(path, []byte("  2.34 invalid 1.98 3/512 12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	load, err := readLoadAvg1FromPath(path)
	if err != nil || load != 2.34 {
		t.Fatalf("readLoadAvg1FromPath = (%v, %v), want (2.34, nil)", load, err)
	}
}

func TestReadLoadAvg1ReadsProcLoadavg(t *testing.T) {
	t.Parallel()
	load, err := readLoadAvg1()
	if err != nil || load < 0 {
		t.Fatalf("readLoadAvg1 = (%v, %v), want nonnegative proc load", load, err)
	}
}
