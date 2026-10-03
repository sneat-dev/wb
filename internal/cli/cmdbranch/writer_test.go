package cmdbranch

// Each family test owns its writer state; errors retain their identity.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type failAfterWriter struct {
	remaining int
	err       error
}

func (w *failAfterWriter) Write(raw []byte) (int, error) {
	if w.remaining == 0 {
		return 0, w.err
	}
	w.remaining--
	return len(raw), nil
}
