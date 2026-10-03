package continuationinput

import (
	"github.com/sneat-dev/wb/internal/unixcompat"
	"io"
	"os"
	"path/filepath"
)

type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}
type HandoverFile interface {
	io.Reader
	io.Closer
	Stat() (os.FileInfo, error)
}

// Effects contains the concrete descriptor and file operations used by Reader.
// On successful OpenAt, File must wrap the same live identity into a nonnil
// owned reader. Native Unix and Windows open success both satisfy this contract.
type Effects struct {
	Abs           func(string) (string, error)
	ResolveParent func(string) (string, error)
	Open          func(string, int) (int, error)
	OpenAt        func(int, string, int) (int, error)
	Close         func(int) error
	Fstat         func(int, *unix.Stat_t) error
	File          func(uintptr, string) ReadSeekCloser
	OpenHandover  func(string) (HandoverFile, error)
}

func DefaultEffects() Effects {
	return Effects{
		Abs: filepath.Abs, ResolveParent: filepath.EvalSymlinks,
		Open:   func(path string, flags int) (int, error) { return unix.Open(path, flags, 0) },
		OpenAt: func(fd int, path string, flags int) (int, error) { return unix.Openat(fd, path, flags, 0) },
		Close:  unix.Close, Fstat: unix.Fstat,
		File:         func(fd uintptr, name string) ReadSeekCloser { return os.NewFile(fd, name) },
		OpenHandover: func(path string) (HandoverFile, error) { return os.Open(path) },
	}
}

type Reader struct{ effects Effects }

func New(effects Effects) *Reader { return &Reader{effects: effects} }
func ReadRegularFile(path string, limit int) ([]byte, error) {
	return New(DefaultEffects()).ReadRegularFile(path, limit)
}
func ReadHandover(input io.Reader, path string, limit int) ([]byte, error) {
	return New(DefaultEffects()).ReadHandover(input, path, limit)
}
