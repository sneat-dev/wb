//go:build !darwin

package checkoutmarker

func normalizeExcludeParent(path string) string { return path }
