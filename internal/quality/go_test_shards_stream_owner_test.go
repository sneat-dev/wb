package quality

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCoverageStreamOwnerPreservesRepeatedCountsAndZeroBlocks(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"set", "count", "atomic"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			a, b, out := filepath.Join(dir, "a.cov"), filepath.Join(dir, "b.cov"), filepath.Join(dir, "out.cov")
			writeCoverageFixture(t, a, "mode: "+mode+"\nz.go:1.1,2.2 2 0\na.go:1.1,2.2 1 3\na.go:1.1,2.2 1 2\na.go:1.1,2.2 1 0\n")
			writeCoverageFixture(t, b, "mode: "+mode+"\na.go:1.1,2.2 1 4\nz.go:1.1,2.2 2 0\n")
			gotMode, rows, err := readCoverageProfile(a)
			if err != nil || gotMode != mode || len(rows) != 4 || rows[1].count != 3 || rows[2].count != 2 || rows[3].count != 0 {
				t.Fatalf("read=%s %+v %v", gotMode, rows, err)
			}
			var visited []coverageBlock
			var modes []string
			scanned, err := scanCoverageProfile(a, func(m string, b coverageBlock) { modes = append(modes, m); visited = append(visited, b) })
			if err != nil || scanned != mode || !reflect.DeepEqual(visited, rows) || !reflect.DeepEqual(modes, []string{mode, mode, mode, mode}) {
				t.Fatalf("scan=%s %+v modes=%v err=%v", scanned, visited, modes, err)
			}
			if err := mergeCoverageProfiles([]string{a, b}, out); err != nil {
				t.Fatal(err)
			}
			count := 9
			if mode == "set" {
				count = 4
			}
			want := fmt.Sprintf("mode: %s\na.go:1.1,2.2 1 %d\nz.go:1.1,2.2 2 0\n", mode, count)
			data, err := os.ReadFile(out)
			if err != nil || string(data) != want {
				t.Fatalf("canonical=%q err=%v want=%q", data, err, want)
			}
		})
	}
}

func TestCoverageStreamOwnerPreservesErrorPrecedenceAndOutput(t *testing.T) {
	t.Parallel()
	rows := []struct{ name, first, second, want string }{
		{"duplicate conflict", "mode: set\na.go:1.1,2.2 1 0\na.go:1.1,2.2 2 1\na.go:1.1,2.2 3 1\n", "", "statement count 2, want 1"},
		{"late malformed outranks conflict", "mode: count\na.go:1.1,2.2 1 0\na.go:1.1,2.2 2 1\na.go:1.1,2.2 1 9\nmalformed\n", "", "at line 5"},
		{"late scanner outranks conflict", "mode: atomic\na.go:1.1,2.2 1 0\na.go:1.1,2.2 2 1\n" + strings.Repeat("x", 70*1024), "", "read coverage profile"},
		{"mode mismatch", "mode: set\na.go:1.1,2.2 1 0\n", "mode: count\na.go:1.1,2.2 2 1\na.go:1.1,2.2 3 1\n", "coverage mode mismatch"},
		{"late malformed outranks mode", "mode: set\na.go:1.1,2.2 1 0\n", "mode: count\na.go:1.1,2.2 2 1\nmalformed\n", "at line 3"},
		{"late scanner outranks mode", "mode: set\na.go:1.1,2.2 1 0\n", "mode: atomic\na.go:1.1,2.2 2 1\n" + strings.Repeat("x", 70*1024), "read coverage profile"},
		{"cross input conflict", "mode: set\na.go:1.1,2.2 1 0\n", "mode: set\na.go:1.1,2.2 2 1\n", "statement count 2, want 1"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			a, b, out := filepath.Join(dir, "a.cov"), filepath.Join(dir, "b.cov"), filepath.Join(dir, "out.cov")
			writeCoverageFixture(t, a, row.first)
			writeCoverageFixture(t, out, "previous durable output\n")
			paths := []string{a}
			if row.second != "" {
				writeCoverageFixture(t, b, row.second)
				paths = append(paths, b)
			}
			err := mergeCoverageProfiles(paths, out)
			if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("error=%v want=%s", err, row.want)
			}
			data, readErr := os.ReadFile(out)
			if readErr != nil || string(data) != "previous durable output\n" {
				t.Fatalf("published partial=%q err=%v", data, readErr)
			}
		})
	}
}

func TestCoverageStreamOwnerValidatesAllNativeInputShapes(t *testing.T) {
	t.Parallel()
	rows := []struct{ name, data, want string }{
		{"empty", "", "is empty"}, {"header shape", "set\n", "invalid mode header"}, {"mode", "mode: other\n", "unsupported coverage mode"},
		{"statement text", "mode: set\na.go:1.1,2.2 no 0\n", "at line 2"}, {"negative statements", "mode: set\na.go:1.1,2.2 -1 0\n", "at line 2"},
		{"count text", "mode: set\na.go:1.1,2.2 1 no\n", "at line 2"}, {"negative count", "mode: set\na.go:1.1,2.2 1 -1\n", "at line 2"},
		{"blank row", "mode: set\n\n", "at line 2"}, {"extra field", "mode: set\na.go:1.1,2.2 1 0 extra\n", "at line 2"},
		{"partial then malformed", "mode: set\na.go:1.1,2.2 1 0\nmalformed\n", "at line 3"},
		{"header scanner", "" + strings.Repeat("x", 70*1024), "read coverage profile"},
		{"body scanner", "mode: set\n" + strings.Repeat("x", 70*1024), "read coverage profile"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "input.cov")
			writeCoverageFixture(t, path, row.data)
			mode, blocks, err := readCoverageProfile(path)
			if err == nil || !strings.Contains(err.Error(), row.want) || mode != "" || blocks != nil {
				t.Fatalf("read=%s %+v %v", mode, blocks, err)
			}
		})
	}
	t.Run("missing native path", func(t *testing.T) {
		t.Parallel()
		_, blocks, err := readCoverageProfile(filepath.Join(t.TempDir(), "missing.cov"))
		if err == nil || !strings.Contains(err.Error(), "open coverage profile") || blocks != nil {
			t.Fatalf("blocks=%v err=%v", blocks, err)
		}
	})
	t.Run("directory read error", func(t *testing.T) {
		t.Parallel()
		_, blocks, err := readCoverageProfile(t.TempDir())
		if err == nil || blocks != nil {
			t.Fatalf("blocks=%v err=%v", blocks, err)
		}
	})
	t.Run("empty merge input", func(t *testing.T) {
		t.Parallel()
		if err := mergeCoverageProfiles(nil, filepath.Join(t.TempDir(), "out.cov")); err == nil || !strings.Contains(err.Error(), "at least one") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("native output staging refusal", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "in.cov")
		writeCoverageFixture(t, input, "mode: set\na.go:1.1,2.2 1 0\n")
		if err := mergeCoverageProfiles([]string{input}, filepath.Join(dir, "missing", "out.cov")); err == nil || !strings.Contains(err.Error(), "create merged coverage") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("header-only remains nil", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input, out := filepath.Join(dir, "in.cov"), filepath.Join(dir, "out.cov")
		writeCoverageFixture(t, input, "mode: atomic\n")
		mode, blocks, err := readCoverageProfile(input)
		if err != nil || mode != "atomic" || blocks != nil {
			t.Fatalf("read=%s %+v %v", mode, blocks, err)
		}
		if err := mergeCoverageProfiles([]string{input}, out); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out)
		if err != nil || string(data) != "mode: atomic\n" {
			t.Fatalf("output=%q %v", data, err)
		}
	})
	t.Run("legacy unchecked count addition", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input, out := filepath.Join(dir, "in.cov"), filepath.Join(dir, "out.cov")
		writeCoverageFixture(t, input, "mode: count\na.go:1.1,2.2 1 9223372036854775807\na.go:1.1,2.2 1 1\n")
		if err := mergeCoverageProfiles([]string{input}, out); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out)
		if err != nil || string(data) != "mode: count\na.go:1.1,2.2 1 -9223372036854775808\n" {
			t.Fatalf("overflow behavior=%q %v", data, err)
		}
	})
}
