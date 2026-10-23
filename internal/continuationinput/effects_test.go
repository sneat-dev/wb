package continuationinput

import (
	"bytes"
	"errors"
	"github.com/sneat-dev/wb/internal/unixcompat"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type observedFile struct {
	first, second                         []byte
	reader                                *bytes.Reader
	round                                 int
	reads                                 int
	firstErr, secondErr, seekErr, statErr error
	events                                *[]string
}

func (f *observedFile) Read(p []byte) (int, error) {
	f.reads++
	if f.round == 0 && f.firstErr != nil {
		return 0, f.firstErr
	}
	if f.round > 0 && f.secondErr != nil {
		return 0, f.secondErr
	}
	return f.reader.Read(p)
}
func (f *observedFile) Seek(offset int64, whence int) (int64, error) {
	*f.events = append(*f.events, "seek")
	if f.seekErr != nil {
		return 0, f.seekErr
	}
	f.round++
	f.reader = bytes.NewReader(f.second)
	return f.reader.Seek(offset, whence)
}
func (f *observedFile) Close() error               { *f.events = append(*f.events, "file-close"); return nil }
func (f *observedFile) Stat() (os.FileInfo, error) { return regularInfo{}, f.statErr }

type regularInfo struct{}

func (regularInfo) Name() string       { return "file" }
func (regularInfo) Size() int64        { return 5 }
func (regularInfo) Mode() os.FileMode  { return 0o600 }
func (regularInfo) ModTime() time.Time { return time.Time{} }
func (regularInfo) IsDir() bool        { return false }
func (regularInfo) Sys() any           { return nil }

func descriptorFixture() (Effects, *observedFile, *[]string) {
	events := []string{}
	file := &observedFile{first: []byte("carry"), second: []byte("carry"), reader: bytes.NewReader([]byte("carry")), events: &events}
	openAtCalls := 0
	effects := Effects{
		Abs:           func(path string) (string, error) { return "/parent/file", nil },
		ResolveParent: func(path string) (string, error) { return "/parent", nil },
		Open:          func(path string, flags int) (int, error) { events = append(events, "open-root"); return 10, nil },
		OpenAt: func(fd int, path string, flags int) (int, error) {
			openAtCalls++
			events = append(events, "openat")
			return 10 + openAtCalls, nil
		},
		Close: func(fd int) error {
			if fd == 10 {
				events = append(events, "close-root")
			} else {
				events = append(events, "close-parent")
			}
			return nil
		},
		Fstat: func(fd int, stat *unix.Stat_t) error {
			events = append(events, "stat")
			*stat = unix.Stat_t{Mode: unix.S_IFREG | 0o600, Nlink: 1, Size: 5}
			return nil
		},
		File:         func(fd uintptr, name string) ReadSeekCloser { events = append(events, "wrap"); return file },
		OpenHandover: func(path string) (HandoverFile, error) { return file, nil },
	}
	return effects, file, &events
}

func TestDescriptorFailuresReleaseOwnedResourcesAndStopLaterEffects(t *testing.T) {
	t.Parallel()
	want := errors.New("descriptor operation unavailable")
	for _, stage := range []string{"abs", "parent-resolution", "root-open", "parent-open", "file-open", "initial-stat", "first-read", "rewind", "second-read", "final-stat"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			effects, file, events := descriptorFixture()
			expected := []string{}
			switch stage {
			case "abs":
				effects.Abs = func(string) (string, error) { return "", want }
			case "parent-resolution":
				effects.ResolveParent = func(string) (string, error) { return "", want }
			case "root-open":
				effects.Open = func(string, int) (int, error) { *events = append(*events, "open-root"); return -1, want }
				expected = []string{"open-root"}
			case "parent-open":
				effects.OpenAt = func(int, string, int) (int, error) { *events = append(*events, "openat"); return -1, want }
				expected = []string{"open-root", "openat", "close-root"}
			case "file-open":
				original := effects.OpenAt
				calls := 0
				effects.OpenAt = func(fd int, path string, flags int) (int, error) {
					calls++
					if calls == 2 {
						*events = append(*events, "openat")
						return -1, want
					}
					return original(fd, path, flags)
				}
				expected = []string{"open-root", "openat", "close-root", "openat", "close-parent"}
			case "initial-stat":
				effects.Fstat = func(int, *unix.Stat_t) error { *events = append(*events, "stat"); return want }
			case "first-read":
				file.firstErr = want
			case "rewind":
				file.seekErr = want
			case "second-read":
				file.secondErr = want
			case "final-stat":
				original := effects.Fstat
				calls := 0
				effects.Fstat = func(fd int, stat *unix.Stat_t) error {
					calls++
					if calls == 2 {
						*events = append(*events, "stat")
						return want
					}
					return original(fd, stat)
				}
			}
			_, err := New(effects).ReadRegularFile("input", 64)
			if stage == "abs" {
				if err == nil || !strings.Contains(err.Error(), "one clean path") {
					t.Fatal(err)
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("error identity: %v", err)
			}
			if len(expected) > 0 || stage == "abs" || stage == "parent-resolution" {
				if !reflect.DeepEqual(*events, expected) {
					t.Fatalf("events=%v want%v", *events, expected)
				}
			} else {
				if len(*events) == 0 || (*events)[len(*events)-1] != "file-close" {
					t.Fatalf("owned file was not released: %v", *events)
				}
				if stage == "initial-stat" && file.reads != 0 {
					t.Fatal("read after failed initial stat")
				}
				if stage == "first-read" && file.round != 0 {
					t.Fatal("seek after failed first read")
				}
				if stage == "rewind" && file.round != 0 {
					t.Fatal("second read after failed rewind")
				}
			}
		})
	}
}

func TestDescriptorMetadataAndRepeatedBytesRefuseTampering(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"directory", "multiple-links", "empty", "oversized", "first-growth", "second-empty", "second-growth", "changed-bytes", "device", "inode", "mode", "links", "size", "unclean-absolute"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			effects, file, events := descriptorFixture()
			original := effects.Fstat
			calls := 0
			effects.Fstat = func(fd int, stat *unix.Stat_t) error {
				calls++
				if err := original(fd, stat); err != nil {
					return err
				}
				if calls == 1 {
					switch stage {
					case "directory":
						stat.Mode = unix.S_IFDIR
					case "multiple-links":
						stat.Nlink = 2
					case "empty":
						stat.Size = 0
					case "oversized":
						stat.Size = 65
					}
				} else {
					switch stage {
					case "device":
						stat.Dev++
					case "inode":
						stat.Ino++
					case "mode":
						stat.Mode++
					case "links":
						stat.Nlink++
					case "size":
						stat.Size++
					}
				}
				return nil
			}
			switch stage {
			case "first-growth":
				file.reader = bytes.NewReader(bytes.Repeat([]byte("x"), 65))
			case "second-empty":
				file.second = nil
			case "second-growth":
				file.second = bytes.Repeat([]byte("x"), 65)
			case "changed-bytes":
				file.second = []byte("other")
			case "unclean-absolute":
				effects.Abs = func(string) (string, error) { return "/parent/../file", nil }
			}
			if _, err := New(effects).ReadRegularFile("input", 64); err == nil {
				t.Fatal("unsafe input accepted")
			}
			if stage == "unclean-absolute" {
				if len(*events) != 0 {
					t.Fatalf("opened unclean path: %v", *events)
				}
				return
			}
			if (*events)[len(*events)-1] != "file-close" {
				t.Fatalf("file leaked: %v", *events)
			}
			if stage == "directory" || stage == "multiple-links" || stage == "empty" || stage == "oversized" {
				if file.reads != 0 {
					t.Fatal("read unsafe metadata")
				}
			}
		})
	}
}

func TestHandoverStatAndReadFailuresCloseTheSeparateOpenedFile(t *testing.T) {
	t.Parallel()
	want := errors.New("handover unavailable")
	for _, stage := range []string{"stat", "read"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			effects, file, events := descriptorFixture()
			if stage == "stat" {
				file.statErr = want
			} else {
				file.firstErr = want
			}
			if _, err := New(effects).ReadHandover(nil, "brief", 64); !errors.Is(err, want) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*events, []string{"file-close"}) {
				t.Fatalf("events=%v", *events)
			}
			if stage == "stat" && file.reads != 0 {
				t.Fatal("read after failed stat")
			}
		})
	}
}
