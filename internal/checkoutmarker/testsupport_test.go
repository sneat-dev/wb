package checkoutmarker

// Helpers kept for tests only: no production caller remains.

// Changed reports whether anything on disk moved.
func (r Result) Changed() bool { return r.MarkerWritten || r.ExcludeWritten }
