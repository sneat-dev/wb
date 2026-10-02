package quality

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidationCacheNativeReadAndPublicationErrors(t *testing.T) {
	t.Parallel()
	key := ValidationCacheKey{Repository: "repo", TargetRevision: "rev"}
	root := t.TempDir()
	path := filepath.Join(root, validationCacheKeyDigest(key)+".json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := LoadValidationCache(root, key); err == nil || hit {
		t.Fatalf("directory record = hit %v error %v", hit, err)
	}
	cacheFile := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(cacheFile, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	report := VerificationReport{Revision: "rev", WorkspaceClean: true, Status: StatusPassed}
	if err := SaveValidationCache(cacheFile, key, report); err == nil {
		t.Fatal("occupied cache root accepted")
	}
	raw, err := os.ReadFile(cacheFile)
	if err != nil || string(raw) != "retained" {
		t.Fatalf("cache root occupant = %q %v", raw, err)
	}
	report.Status = StatusSkipped
	if err := SaveValidationCache(root, key, report); err == nil {
		t.Fatal("skipped report published")
	}
}

func TestValidationCacheIntegrityChecksRemainIndependent(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"none", "malformed", "schema", "key", "skipped", "revision", "dirty", "digest"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			key := ValidationCacheKey{Repository: "repo", TargetRevision: "rev", ValidatorSHAs: map[string]string{"specscore": "binary"}}
			report := VerificationReport{Revision: "rev", WorkspaceClean: true, Status: StatusFailed, Results: []VerificationEntry{{Check: CheckLint, Deadcode: &DeadcodeFailureEvidence{Count: 1, Identities: []string{"pkg.Function"}, Complete: true}}}}
			record := validationCacheRecord{Schema: validationCacheSchema, Key: key, Report: report}
			record.Digest = validationCacheDigest(record.Schema, key, report)
			switch mutation {
			case "schema":
				record.Schema++
			case "key":
				record.Key.Repository = "other"
			case "skipped":
				record.Report.Status = StatusSkipped
			case "revision":
				record.Report.Revision = "other"
			case "dirty":
				record.Report.WorkspaceClean = false
			case "digest":
				record.Digest = "tampered"
			}
			// Seal each invalid record coherently, so its specific metadata gate
			// must refuse it independently of the digest-integrity check.
			if mutation != "digest" && mutation != "malformed" {
				record.Digest = validationCacheDigest(record.Schema, record.Key, record.Report)
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "malformed" {
				raw = []byte("{")
			}
			if err := os.WriteFile(filepath.Join(root, validationCacheKeyDigest(key)+".json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			got, hit, err := LoadValidationCache(root, key)
			if err != nil || hit != (mutation == "none") {
				t.Fatalf("load = %+v hit %v err %v", got, hit, err)
			}
			if hit && (got.Status != StatusFailed || len(got.Results) != 1 || !got.Results[0].Deadcode.Valid()) {
				t.Fatalf("complete failed evidence lost = %+v", got)
			}
		})
	}
}
