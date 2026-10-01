package machinemetrics

// unsupportedSource is the Source of a platform with no reader.
type unsupportedSource struct{}

// Read always reports ErrUnsupported.
func (unsupportedSource) Read() (Sample, error) { return Sample{}, ErrUnsupported }
