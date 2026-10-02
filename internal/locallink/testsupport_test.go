package locallink

import "os"

// Helpers kept for tests only: no production caller remains.

func copyBuiltPackageContents(source, destination string) error {
	return copyBuiltPackageContentsInjected(source, destination, nil)
}

func copyBuiltPackage(source, destination string) error {
	if err := validateBuiltPackageSource(source); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		return err
	}
	return copyBuiltPackageContents(source, destination)
}
