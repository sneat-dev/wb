package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/syncreport"
)

type SyncReportPublishResult struct {
	CommitSHA string   `json:"commit_sha"`
	Paths     []string `json:"paths"`
}

// PublishSyncReport installs a validated collection schema and one Markdown
// record per repository, validates the resulting InGitDB checkout, commits
// only owned paths, and pushes through the state repository's serialized
// rebase-on-rejection path.
func (p *Provider) PublishSyncReport(ctx context.Context, report syncreport.Report, validate func(context.Context, string) error) (SyncReportPublishResult, error) {
	lock, err := acquireCloneLock(p.opts.ClonePath)
	if err != nil {
		return SyncReportPublishResult{}, err
	}
	defer func() { _ = lock.release() }()
	if err := p.Fetch(ctx); err != nil {
		return SyncReportPublishResult{}, err
	}
	status, err := gitops.Status(p.opts.ClonePath)
	if err != nil {
		return SyncReportPublishResult{}, err
	}
	if status.Dirty() {
		return SyncReportPublishResult{}, fmt.Errorf("workbench repository is not safe to update: %s", status.Summary())
	}

	schemaPaths := []string{syncreport.SettingsPath, syncreport.RootCollectionsPath, syncreport.DefinitionPath}
	paths := append([]string(nil), schemaPaths...)
	contents := map[string][]byte{
		syncreport.SettingsPath:        []byte(syncreport.SettingsYAML),
		syncreport.RootCollectionsPath: []byte(syncreport.RootCollectionsYAML),
		syncreport.DefinitionPath:      []byte(syncreport.DefinitionYAML),
	}
	for _, record := range report.Records {
		relative := filepath.ToSlash(filepath.Join(syncreport.CollectionRecords, syncreport.RecordName(report.ID, record.Repository)))
		paths = append(paths, relative)
		contents[relative] = record.Raw
	}

	type priorFile struct {
		data    []byte
		existed bool
	}
	prior := map[string]priorFile{}
	rollback := func() {
		for relative, before := range prior {
			absolute := filepath.Join(p.opts.ClonePath, filepath.FromSlash(relative))
			if before.existed {
				_ = os.WriteFile(absolute, before.data, 0o644)
			} else {
				_ = os.Remove(absolute)
			}
		}
	}
	for _, relative := range paths {
		absolute := filepath.Join(p.opts.ClonePath, filepath.FromSlash(relative))
		before, readErr := os.ReadFile(absolute)
		switch {
		case readErr == nil:
			prior[relative] = priorFile{data: before, existed: true}
			if relative == syncreport.RootCollectionsPath {
				merged, changed, err := syncreport.MergeRootCollections(before)
				if err != nil {
					return SyncReportPublishResult{}, err
				}
				if !changed {
					continue
				}
				contents[relative] = merged
			}
			if relative == syncreport.DefinitionPath && string(before) != syncreport.DefinitionYAML {
				return SyncReportPublishResult{}, errors.New("workbench repository has an incompatible sync-reports collection definition")
			}
			if strings.HasPrefix(relative, syncreport.CollectionRecords+"/") && !bytes.Equal(before, contents[relative]) {
				return SyncReportPublishResult{}, fmt.Errorf("sync report record %s already exists with different contents; report records are immutable", relative)
			}
			// Preserve an existing root database setting; the collection owns its
			// own explicit Markdown format and does not need to replace it.
			if relative == syncreport.SettingsPath {
				continue
			}
		case errors.Is(readErr, os.ErrNotExist):
			prior[relative] = priorFile{}
		default:
			return SyncReportPublishResult{}, readErr
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			rollback()
			return SyncReportPublishResult{}, err
		}
		if err := os.WriteFile(absolute, contents[relative], 0o644); err != nil {
			rollback()
			return SyncReportPublishResult{}, err
		}
	}
	if err := validate(ctx, p.opts.ClonePath); err != nil {
		rollback()
		return SyncReportPublishResult{}, err
	}
	message := fmt.Sprintf("wb: publish sync report %s (%d repositories)", report.ID, len(report.Records))
	committed, err := gitops.AddCommit(p.opts.ClonePath, message, paths...)
	if err != nil {
		rollback()
		return SyncReportPublishResult{}, err
	}
	if committed {
		if err := p.push(); err != nil {
			if rebaseErr := gitops.PullRebase(p.opts.ClonePath); rebaseErr != nil {
				if detail, was := abortDetailIfRebasing(p.opts.ClonePath); was {
					return SyncReportPublishResult{}, fmt.Errorf("push rejected and rebase failed (%s): %w", detail, rebaseErr)
				}
				return SyncReportPublishResult{}, fmt.Errorf("push rejected and rebase failed: %w", rebaseErr)
			}
			if err := p.push(); err != nil {
				return SyncReportPublishResult{}, fmt.Errorf("push rejected twice; local commit kept: %w", err)
			}
		}
	}
	sha, err := gitops.HeadSHA(p.opts.ClonePath)
	if err != nil {
		return SyncReportPublishResult{}, err
	}
	return SyncReportPublishResult{CommitSHA: sha, Paths: paths[len(schemaPaths):]}, nil
}
