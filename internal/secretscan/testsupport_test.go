package secretscan

// Helpers kept for tests only: no production caller remains.

// Rules returns every loaded rule, for diagnostics (e.g. `wb` printing what
// a scan ran with). Callers must not mutate the result.
func (s *Scanner) Rules() []Rule {
	return s.rules
}
