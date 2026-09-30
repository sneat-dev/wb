package worktreeclaims

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const MaxFinalizeReportBytes = 1 << 20

type FinalizeReportPorts struct {
	SplitRepository func(string) (string, string, error)
	ValidSegment    func(string) bool
	OpenRun         func(home, effort, run string, create bool) (*os.File, string, error)
	OpenChild       func(*os.File, string, bool) (*os.File, error)
	WriteBytes      func(*os.File, string, []byte, os.FileMode) error
	OpenDirectory   func(string, bool) (*os.File, error)
	ReadBytes       func(*os.File, string) ([]byte, error)
}

func (p FinalizeReportPorts) FinalizeReportFileName(task, repository string) (string, error) {
	owner, name, err := p.SplitRepository(repository)
	if err != nil {
		return "", fmt.Errorf("resolve report file name: %w", err)
	}
	task = strings.TrimSpace(task)
	if !p.ValidSegment(task) {
		return "", fmt.Errorf("invalid task identity %q for report file name", task)
	}
	return task + "--" + owner + "--" + name + ".md", nil
}

func (p FinalizeReportPorts) WriteFinalizeReport(home, effort, run, task, repository string, body []byte) (string, error) {
	if len(body) > MaxFinalizeReportBytes {
		return "", fmt.Errorf("finalize report exceeds %d bytes (%d MiB cap)", MaxFinalizeReportBytes, MaxFinalizeReportBytes/(1<<20))
	}
	fileName, err := p.FinalizeReportFileName(task, repository)
	if err != nil {
		return "", err
	}
	runDir, runPath, err := p.OpenRun(home, effort, run, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = runDir.Close() }()
	reports, err := p.OpenChild(runDir, "reports", true)
	if err != nil {
		return "", err
	}
	defer func() { _ = reports.Close() }()
	if err := p.WriteBytes(reports, fileName, body, 0o600); err != nil {
		return "", fmt.Errorf("write finalize report: %w", err)
	}
	return filepath.Join(runPath, "reports", fileName), nil
}

func (p FinalizeReportPorts) ReadFinalizeReportBody(reportPath string) (string, error) {
	directory, err := p.OpenDirectory(filepath.Dir(reportPath), false)
	if err != nil {
		return "", err
	}
	defer func() { _ = directory.Close() }()
	content, err := p.ReadBytes(directory, filepath.Base(reportPath))
	if err != nil {
		return "", err
	}
	return string(content), nil
}
