package migrate

// StructuralAdapter performs syntax-aware migration operations for one
// language. The engine deliberately does not attempt to normalize Go, Python,
// and TypeScript ASTs into one representation; each adapter owns correctness
// for its syntax, imports, and name binding.
type StructuralAdapter interface {
	Transform(source []byte, filename string, step Step) (updated []byte, changed bool, err error)
}

var structuralAdapters = map[string]StructuralAdapter{
	"go": goAdapter{},
}

type goAdapter struct{}

func (goAdapter) Transform(source []byte, filename string, step Step) ([]byte, bool, error) {
	return transformGo(source, filename, step)
}
