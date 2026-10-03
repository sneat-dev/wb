package sessionrun

import (
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"path/filepath"
)

func DirForRead(projectsRoot string) (string, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, session.DirName), nil
}
func DirForWrite(projectsRoot string) (string, error) {
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, session.DirName), nil
}
