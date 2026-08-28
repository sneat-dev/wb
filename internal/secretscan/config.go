package secretscan

import (
	"fmt"
	"os"
	"path/filepath"
)

// ExtraRulesEnvVar names an additional gitleaks-schema rules file to load on
// top of the embedded baseline. Set it to point at a refreshed
// config/gitleaks.toml (see gitleaks/PROVENANCE.md) to pick up new
// upstream patterns without a WB release, or at a small file of
// internal-only token shapes.
const ExtraRulesEnvVar = "WB_SECRETSCAN_RULES"

// LoadOptions controls where LoadDefault looks for the operator-extensible
// rules file, beyond the WB binary's embedded baseline.
type LoadOptions struct {
	// EnvExtraRulesPath overrides ExtraRulesEnvVar's value, for tests.
	// Empty means "read the real environment variable".
	EnvExtraRulesPath *string
	// UserConfigDir overrides the user-level config directory (normally
	// os.UserConfigDir), for tests.
	UserConfigDir string
}

// UserRulesPath returns the default user-level extra rules path:
// <config dir>/wb/secretscan/rules.toml. It never errors when the config
// directory cannot be determined -- it just reports found=false, since this
// path is optional.
func UserRulesPath(userConfigDir string) (path string, found bool) {
	dir := userConfigDir
	if dir == "" {
		var err error
		dir, err = os.UserConfigDir()
		if err != nil {
			return "", false
		}
	}
	return filepath.Join(dir, "wb", "secretscan", "rules.toml"), true
}

// LoadDefault builds the Scanner WB uses in production: the embedded
// gitleaks-derived baseline, plus whichever extra rules file is configured
// (env var first, then the user-level default path), if it exists.
// A missing extra rules file is not an error -- it is the common case.
func LoadDefault(options LoadOptions) (*Scanner, []string, error) {
	rules, skipped, err := parseTOMLRuleset(EmbeddedRuleset(), "gitleaks-embedded", classifyEmbeddedRule)
	if err != nil {
		return nil, nil, fmt.Errorf("load embedded secret scan ruleset: %w", err)
	}

	extraPath := ""
	if options.EnvExtraRulesPath != nil {
		extraPath = *options.EnvExtraRulesPath
	} else {
		extraPath = os.Getenv(ExtraRulesEnvVar)
	}
	if extraPath == "" {
		if path, found := UserRulesPath(options.UserConfigDir); found {
			if _, statErr := os.Stat(path); statErr == nil {
				extraPath = path
			}
		}
	}

	if extraPath != "" {
		extraRules, extraSkipped, loadErr := LoadRulesFile(extraPath)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		rules = append(rules, extraRules...)
		skipped = append(skipped, extraSkipped...)
	}

	return NewScanner(rules), skipped, nil
}

// LoadRulesFile parses one operator-supplied extra rules file. It uses the
// same [[rules]] TOML schema gitleaks itself defines, plus one WB-only
// optional field per rule, `severity = "warn"` (see classifyExtraRule).
func LoadRulesFile(path string) ([]Rule, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read secret scan rules file %s: %w", path, err)
	}
	return parseTOMLRuleset(data, path, classifyExtraRule)
}
