package githubapp

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	readmeOptInHeading = regexp.MustCompile(`^[ \t]{0,3}##[ \t]+(WB|Workbench)(?:[ \t]+#+)?[ \t]*$`)
	markdownLink       = regexp.MustCompile(`\[[^\]]*\]\(\s*(?:<)?(https://[^\s)>]+)`)
	autolink           = regexp.MustCompile(`<((?:https://)[^\s>]+)>`)
	githubName         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	gitCommitSHA       = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// VerifyPublicEligibility returns auditable evidence only when the root README
// has an explicit Workbench opt-in. The caller supplies the canonical GitHub
// README URL it read and the verification time from its authoritative refresh.
func VerifyPublicEligibility(repository, readmeURL, markdown string, verifiedAt time.Time) (PublicEligibility, error) {
	evidence := PublicEligibility{
		Repository: repository,
		READMEURL:  readmeURL,
		VerifiedAt: verifiedAt.UTC(),
	}
	if err := ValidatePublicEligibility(evidence); err != nil {
		return PublicEligibility{}, err
	}
	if !readmeHasWorkbenchOptIn(markdown) {
		return PublicEligibility{}, errors.New("root README does not contain a Workbench opt-in link")
	}
	return evidence, nil
}

// ValidatePublicEligibility rejects incomplete or non-canonical durable
// evidence. It intentionally verifies the identity and URL shape only; the
// authoritative reader verifies the README contents before it records this
// evidence with a projection.
func ValidatePublicEligibility(evidence PublicEligibility) error {
	owner, repository, err := canonicalGitHubRepository(evidence.Repository)
	if err != nil {
		return err
	}
	if evidence.VerifiedAt.IsZero() {
		return errors.New("public eligibility verified_at is required")
	}
	readme, err := url.Parse(evidence.READMEURL)
	if err != nil {
		return fmt.Errorf("parse public eligibility README URL: %w", err)
	}
	if readme.Scheme != "https" || readme.Host != "github.com" || readme.User != nil || readme.RawQuery != "" || readme.Fragment != "" {
		return errors.New("public eligibility README URL must be a canonical GitHub HTTPS URL")
	}
	prefix := "/" + owner + "/" + repository + "/blob/"
	if !strings.HasPrefix(readme.Path, prefix) || !strings.HasSuffix(readme.Path, "/README.md") {
		return errors.New("public eligibility README URL must identify the repository root README")
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(readme.Path, prefix), "/README.md")
	if !gitCommitSHA.MatchString(ref) {
		return errors.New("public eligibility README URL must identify the root README at an exact 40-hex Git commit SHA")
	}
	return nil
}

func canonicalGitHubRepository(repository string) (string, string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 3 || parts[0] != "github.com" || !githubName.MatchString(parts[1]) || !githubName.MatchString(parts[2]) {
		return "", "", fmt.Errorf("repository must be canonical github.com/<owner>/<repository>, got %q", repository)
	}
	return parts[1], parts[2], nil
}

func readmeHasWorkbenchOptIn(markdown string) bool {
	inSection := false
	fence, fenceLength := byte(0), 0
	for _, line := range strings.Split(markdown, "\n") {
		if marker, markerLength := fenceMarker(line); marker != 0 {
			if fence == 0 {
				fence, fenceLength = marker, markerLength
			} else if fence == marker && markerLength >= fenceLength {
				fence = 0
				fenceLength = 0
			}
			continue
		}
		if fence != 0 {
			continue
		}
		if isLevelOneOrTwoHeading(line) {
			inSection = readmeOptInHeading.MatchString(line)
			continue
		}
		if inSection && hasWorkbenchLink(line) {
			return true
		}
	}
	return false
}

func fenceMarker(line string) (byte, int) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) == 0 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0
	}
	length := 0
	for length < len(trimmed) && trimmed[length] == trimmed[0] {
		length++
	}
	if length < 3 {
		return 0, 0
	}
	return trimmed[0], length
}

func isLevelOneOrTwoHeading(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "#") {
		return false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	return level <= 2 && level < len(trimmed) && (trimmed[level] == ' ' || trimmed[level] == '\t')
}

func hasWorkbenchLink(line string) bool {
	for _, match := range markdownLink.FindAllStringSubmatch(line, -1) {
		if workbenchURL(match[1]) {
			return true
		}
	}
	for _, match := range autolink.FindAllStringSubmatch(line, -1) {
		if workbenchURL(match[1]) {
			return true
		}
	}
	return false
}

func workbenchURL(raw string) bool {
	link, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if link.Scheme != "https" || link.Host != "sneat.work" || link.User != nil || link.RawQuery != "" || link.Fragment != "" {
		return false
	}
	return link.Path == "/bench" || link.Path == "/bench/" || link.Path == "/bench/dashboard" || strings.HasPrefix(link.Path, "/bench/dashboard/") || link.Path == "/bench/repo" || strings.HasPrefix(link.Path, "/bench/repo/")
}
