//go:build darwin || linux

package filewrite

import "os"

func publishImmutableTemporaryAt(directory *os.File, temporary, name string, content []byte, idempotent bool, inj *Injector) (bool, error) {
	if err := RenameNoReplace(int(directory.Fd()), temporary, int(directory.Fd()), name, inj); err != nil {
		return immutablePublicationError(directory, name, content, idempotent, err)
	}
	return true, nil
}
