package sessionpark

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestSourceAggregatePublicationFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		step        filewrite.Step
		artifact    string
		mkdir, open int
	}{
		{name: "aggregate mkdir", mkdir: 1}, {name: "events mkdir", mkdir: 2},
		{name: "aggregate open", open: 1}, {name: "events open", open: 2},
		{name: "bundle write", step: filewrite.StepWrite, artifact: BundleFileName},
		{name: "continuation write", step: filewrite.StepWrite, artifact: sourceContinuationFileName},
		{name: "root chmod", step: filewrite.StepChmod, artifact: "store"},
		{name: "events sync", step: filewrite.StepDirSync, artifact: sourceEventsDirName},
		{name: "aggregate sync", step: filewrite.StepDirSync, artifact: "park-test"},
		{name: "root sync", step: filewrite.StepDirSync, artifact: "store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := NewStore(filepath.Join(t.TempDir(), "store"))
			bundle := testBundle(t)
			refused := errors.New("publication refused")
			inj := &filewrite.Injector{Step: tc.step, Name: tc.artifact, Err: refused}
			if tc.name == "aggregate sync" {
				inj.Skip = 2
			}
			mkdirs, opens := 0, 0
			var owned []*os.File
			mkdirAt := func(fd int, name string, mode uint32) error {
				mkdirs++
				if mkdirs == tc.mkdir {
					return refused
				}
				return unix.Mkdirat(fd, name, mode)
			}
			open := func(parent *os.File, name string) (*os.File, error) {
				opens++
				owned = append(owned, parent)
				if opens == tc.open {
					return nil, refused
				}
				f, err := openPrivateDirectoryAt(parent, name)
				if err == nil {
					owned = append(owned, f)
				}
				return f, err
			}
			result, err := store.create(bundle, mkdirAt, open, inj)
			if !errors.Is(err, refused) || result.ParkedSessionID != "" {
				t.Fatalf("create = %#v, %v", result, err)
			}
			for _, f := range owned {
				if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("owned %s survived refusal: %v", f.Name(), err)
				}
			}
		})
	}
}

func TestTargetAggregatePublicationFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		step        filewrite.Step
		artifact    string
		mkdir, open int
	}{
		{name: "aggregate mkdir", mkdir: 1}, {name: "events mkdir", mkdir: 2},
		{name: "aggregate open", open: 1}, {name: "events open", open: 2},
		{name: "root chmod", step: filewrite.StepChmod, artifact: "wb-park-resume-admit-root"},
		{name: "aggregate chmod", step: filewrite.StepChmod, artifact: "wb-park-resume-admit-aggregate"},
		{name: "events chmod", step: filewrite.StepChmod, artifact: "wb-park-resume-admit-events"},
		{name: "continuation write", step: filewrite.StepWrite, artifact: ContinuationFileName},
		{name: "events sync", step: filewrite.StepDirSync, artifact: "wb-park-resume-admit-events"},
		{name: "aggregate sync", step: filewrite.StepDirSync, artifact: "wb-park-resume-admit-aggregate"},
		{name: "root sync", step: filewrite.StepDirSync, artifact: "wb-park-resume-admit-root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := NewTargetStore(filepath.Join(t.TempDir(), "store"))
			refused := errors.New("target publication refused")
			inj := &filewrite.Injector{Step: tc.step, Name: tc.artifact, Err: refused}
			if tc.name == "aggregate sync" {
				inj.Skip = 2
			}
			mkdirs, opens := 0, 0
			mkdirAt := func(fd int, name string, mode uint32) error {
				mkdirs++
				if mkdirs == tc.mkdir {
					return refused
				}
				return unix.Mkdirat(fd, name, mode)
			}
			openAt := func(fd int, name string, flags int, mode uint32) (int, error) {
				opens++
				if opens == tc.open {
					return -1, refused
				}
				return unix.Openat(fd, name, flags, mode)
			}
			result, err := store.admitWithOperations(targetEnvelopeForTest(t), inj, mkdirAt, openAt, unix.Flock)
			if !errors.Is(err, refused) || result.Digest != "" {
				t.Fatalf("admit = %#v, %v", result, err)
			}
		})
	}
}
