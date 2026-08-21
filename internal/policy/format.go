package policy

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Format is an output rendering.
type Format string

const (
	// FormatText is for a person reading a terminal.
	FormatText Format = "text"
	// FormatJSON is for another program, including fleet aggregation.
	FormatJSON Format = "json"
	// FormatGitHub emits workflow commands so findings land as annotations on
	// the changed lines of a pull request.
	FormatGitHub Format = "github"
)

// ParseFormat validates a format name.
func ParseFormat(raw string) (Format, error) {
	switch Format(raw) {
	case FormatText, FormatJSON, FormatGitHub:
		return Format(raw), nil
	default:
		return "", fmt.Errorf("unknown format %q: expected text, json or github", raw)
	}
}

// WriteResult renders a check result.
func WriteResult(out io.Writer, result Result, format Format) error {
	switch format {
	case FormatJSON:
		return writeJSON(out, resultPayload(result))
	case FormatGitHub:
		return writeGitHub(out, result)
	default:
		return writeText(out, result)
	}
}

func writeText(out io.Writer, result Result) error {
	origin := "declared"
	if result.TypeDetected {
		origin = "detected from the module path"
	}
	if _, err := fmt.Fprintf(out, "%s\n  type %s (%s)\n  policy %s\n\n",
		result.Module.Path, result.Type, origin, result.Policy.Source); err != nil {
		return err
	}
	if len(result.Findings) == 0 {
		_, err := fmt.Fprintf(out, "no violations\n")
		return err
	}

	byMessage := map[string][]Finding{}
	var order []string
	for _, finding := range result.Findings {
		if _, seen := byMessage[finding.Message]; !seen {
			order = append(order, finding.Message)
		}
		byMessage[finding.Message] = append(byMessage[finding.Message], finding)
	}
	for _, message := range order {
		findings := byMessage[message]
		marker := "x"
		suffix := ""
		if findings[0].Mode == ModeReport {
			marker = "!"
			suffix = "  (report only — does not fail this check)"
		}
		if _, err := fmt.Fprintf(out, "%s %s%s\n\n", marker, message, suffix); err != nil {
			return err
		}
		for _, finding := range findings {
			location := finding.File
			if finding.Line > 0 {
				location = fmt.Sprintf("%s:%d", finding.File, finding.Line)
			}
			if _, err := fmt.Fprintf(out, "  %s\n", location); err != nil {
				return err
			}
			if finding.Import != "" {
				if _, err := fmt.Fprintf(out, "      %s\n", finding.Import); err != nil {
					return err
				}
			}
			detail := ""
			switch finding.Rule {
			case RuleImport:
				detail = fmt.Sprintf("group: %s", finding.Group)
				if finding.Manifest {
					detail += "  (required in go.mod)"
				}
			case RuleLayer:
				detail = fmt.Sprintf("%s -> %s", finding.FromRole, finding.ToRole)
			}
			if detail != "" {
				if _, err := fmt.Fprintf(out, "      |- %s\n", detail); err != nil {
					return err
				}
			}
		}
		if fix := findings[0].Fix; fix != "" {
			if _, err := fmt.Fprintf(out, "\n  fix: %s\n", fix); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}

	if len(result.Module.Unparseable) > 0 {
		if _, err := fmt.Fprintf(out, "! %d file(s) could not be parsed and were not checked: %s\n\n",
			len(result.Module.Unparseable), strings.Join(result.Module.Unparseable, ", ")); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "%d blocking, %d reported\n", result.Blocking(), result.Reported())
	return err
}

func writeGitHub(out io.Writer, result Result) error {
	for _, finding := range result.Findings {
		command := "error"
		if finding.Mode == ModeReport {
			command = "notice"
		}
		title := "dependency policy"
		if finding.Rule == RuleLayer {
			title = "layer policy"
		}
		message := finding.Message
		if finding.Import != "" {
			message += " — " + finding.Import
		}
		if finding.Fix != "" {
			message += " (" + finding.Fix + ")"
		}
		if _, err := fmt.Fprintf(out, "::%s file=%s,line=%d,title=%s::%s\n",
			command, finding.File, max(finding.Line, 1), title, escapeWorkflow(message)); err != nil {
			return err
		}
	}
	for _, name := range result.Module.Unparseable {
		if _, err := fmt.Fprintf(out, "::warning file=%s::not parsed, so not checked\n", name); err != nil {
			return err
		}
	}
	return nil
}

// escapeWorkflow protects the delimiters GitHub uses to frame a workflow
// command, so a message containing a newline or a colon cannot truncate or
// forge one.
func escapeWorkflow(message string) string {
	replacer := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", "::", "%3A%3A")
	return replacer.Replace(message)
}

type findingPayload struct {
	Rule     string `json:"rule"`
	Mode     string `json:"mode"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Package  string `json:"package,omitempty"`
	Scope    string `json:"scope"`
	Import   string `json:"import,omitempty"`
	Manifest bool   `json:"manifest,omitempty"`
	Group    string `json:"group,omitempty"`
	FromRole string `json:"fromRole,omitempty"`
	ToRole   string `json:"toRole,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

type resultJSON struct {
	Module       string           `json:"module"`
	Type         string           `json:"type"`
	TypeDetected bool             `json:"typeDetected"`
	Policy       string           `json:"policy"`
	Blocking     int              `json:"blocking"`
	Reported     int              `json:"reported"`
	Unparseable  []string         `json:"unparseable,omitempty"`
	Findings     []findingPayload `json:"findings"`
}

func resultPayload(result Result) resultJSON {
	payload := resultJSON{
		Module:       result.Module.Path,
		Type:         result.Type,
		TypeDetected: result.TypeDetected,
		Policy:       result.Policy.Source,
		Blocking:     result.Blocking(),
		Reported:     result.Reported(),
		Unparseable:  result.Module.Unparseable,
		Findings:     make([]findingPayload, 0, len(result.Findings)),
	}
	for _, finding := range result.Findings {
		payload.Findings = append(payload.Findings, findingPayload{
			Rule: finding.Rule, Mode: string(finding.Mode), File: finding.File, Line: finding.Line,
			Package: finding.Package, Scope: finding.Scope, Import: finding.Import, Manifest: finding.Manifest,
			Group: finding.Group, FromRole: finding.FromRole, ToRole: finding.ToRole,
			Message: finding.Message, Fix: finding.Fix,
		})
	}
	return payload
}

func writeJSON(out io.Writer, payload any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}
