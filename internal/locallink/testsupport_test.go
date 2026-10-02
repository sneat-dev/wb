package locallink

// Helpers kept for tests only: no production caller remains.

func copyBuiltPackageContents(source, destination string) error {
	return copyBuiltPackageContentsInjected(source, destination, nil)
}
