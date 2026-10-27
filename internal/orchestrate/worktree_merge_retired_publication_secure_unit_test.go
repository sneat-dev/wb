package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

// TestRetiredPublicationRefusesInvalidAdmissionAndNamespaceChanges drives the
// acknowledgement publisher's admission and namespace boundaries with real
// private directories: a parent swapped before admission, a time that cannot be
// marshalled, the checked nil-namespace fallback, a nil or foreign bound
// namespace, a swap after the temp file closes, and an acknowledgement read
// whose parent is a symlink. Every refusal must leave no acknowledgement.
func TestRetiredPublicationRefusesInvalidAdmissionAndNamespaceChanges(t *testing.T) {
	t.Parallel()
	t.Run("parent replaced before admission", func(t *testing.T) {
		t.Parallel()
		root, receipt := terminalOwnerPolicyReceipt(t)
		ports := productionRetiredPublicationPorts()
		reads := 0
		ports.readReceipt = func(path string) (WorktreeMergeReceipt, string, error) {
			observed, hash, err := readWorktreeMergeReceiptSnapshot(path)
			if err != nil {
				return WorktreeMergeReceipt{}, "", err
			}
			reads++
			if reads == 1 {
				return observed, hash, nil
			}
			directory := filepath.Dir(path)
			if err := os.Rename(directory, directory+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(directory+"-held", directory); err != nil {
				t.Fatal(err)
			}
			return observed, hash, nil
		}
		_, err := acknowledgeRetiredPublication(context.Background(), WorktreeMergeRetiredPublicationAcknowledgementOptions{
			ProjectsRoot: root, Receipt: receipt.ReceiptPath,
		}, ports)
		if err == nil {
			t.Fatal("acknowledgement admitted a parent replaced before admission")
		}
		if !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("admission refusal = %v, want the swapped parent refusal", err)
		}
	})

	ack := WorktreeMergeRetiredPublicationAcknowledgement{ID: "ack", Status: "retired_publication_acknowledged", RecordedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}

	t.Run("invalid recorded time", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		path := filepath.Join(directory, "ack.json")
		namespace, err := openPublicationNamespace(path, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = namespace.Close() }()
		unmarshalable := ack
		unmarshalable.RecordedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := persistRetiredPublicationAcknowledgementBound(namespace, path, unmarshalable); err == nil {
			t.Fatal("acknowledgement with an unmarshalable time was published")
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed marshal wrote an acknowledgement: %v", statErr)
		}
	})

	t.Run("checked nil namespace fallback", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "ack.json")
		if err := persistRetiredPublicationAcknowledgementBound(nil, path, ack); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("fallback acknowledgement = %v, %v", info, err)
		}
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var decoded WorktreeMergeRetiredPublicationAcknowledgement
		if err := json.Unmarshal(original, &decoded); err != nil || decoded.ID != "ack" {
			t.Fatalf("fallback acknowledgement bytes = %q, %v", original, err)
		}
		if err := persistRetiredPublicationAcknowledgementBound(nil, path, ack); err == nil {
			t.Fatal("fallback replaced existing evidence")
		}
		if after, err := os.ReadFile(path); err != nil || string(after) != string(original) {
			t.Fatalf("fallback replaced bytes: %v", err)
		}
	})

	t.Run("nil namespace bytes", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "ack.json")
		if err := persistRetiredPublicationAcknowledgementBytes(nil, path, []byte("{}\n"), nil); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("nil namespace bytes = %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("nil namespace wrote a file: %v", err)
		}
	})

	t.Run("foreign bound namespace", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		parentA := filepath.Join(root, "a")
		parentB := filepath.Join(root, "b")
		for _, directory := range []string{parentA, parentB} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		namespace, err := openPublicationNamespace(filepath.Join(parentA, "ack.json"), false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = namespace.Close() }()
		foreign := filepath.Join(parentB, "ack.json")
		if err := persistRetiredPublicationAcknowledgementBytes(namespace, foreign, []byte("{}\n"), nil); err == nil || !strings.Contains(err.Error(), "outside its admitted namespace") {
			t.Fatalf("foreign namespace = %v", err)
		}
		if _, err := os.Stat(foreign); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("foreign namespace wrote into parent B: %v", err)
		}
	})

	t.Run("namespace swapped after close", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		held := filepath.Join(root, "evidence")
		if err := os.MkdirAll(held, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(held, "ack.json")
		inj := &filewrite.Injector{Step: filewrite.StepClose, Hook: func() {
			if err := os.Rename(held, held+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(held, 0o700); err != nil {
				t.Fatal(err)
			}
		}}
		if err := persistRetiredPublicationAcknowledgementInjected(path, ack, inj); err == nil || !strings.Contains(err.Error(), "namespace") {
			t.Fatalf("swap after close = %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("swap after close published an acknowledgement: %v", err)
		}
	})

	t.Run("acknowledgement read refuses symlinked parent", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		real := filepath.Join(root, "real")
		if err := os.MkdirAll(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		_, receipt := terminalOwnerPolicyReceipt(t)
		path := filepath.Join(link, "ack.json")
		if _, err := readRetiredPublicationAcknowledgementWithReceiptHash(path, receipt, strings.Repeat("a", 64)); err == nil {
			t.Fatal("acknowledgement read followed a symlinked parent")
		}
	})
}

// TestSecureEvidenceRefusesStaleNamespaceAndClosesDescriptors covers the
// secure reader's own descriptor lifecycle: a namespace replaced immediately
// after its real descriptor is admitted closes that descriptor and refuses, nil
// namespaces verify and close safely, and a parent replaced before the initial
// fence or before an in-namespace read refuses before any byte is returned.
func TestSecureEvidenceRefusesStaleNamespaceAndClosesDescriptors(t *testing.T) {
	t.Parallel()
	t.Run("post-open swap closes the descriptor", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		held := filepath.Join(root, "evidence")
		if err := os.MkdirAll(held, 0o700); err != nil {
			t.Fatal(err)
		}
		var descriptor *os.File
		opener := func(path string, _ bool) (*os.File, error) {
			directory, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(path, false)
			if err != nil {
				return nil, err
			}
			descriptor = directory
			if err := os.Rename(held, held+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(held+"-held", held); err != nil {
				t.Fatal(err)
			}
			return directory, nil
		}
		if _, err := openPublicationNamespaceWith(filepath.Join(held, "ack.json"), false, opener); err == nil || !strings.Contains(err.Error(), "no longer holds") {
			t.Fatalf("post-open swap = %v", err)
		}
		if descriptor == nil {
			t.Fatal("opener never returned a descriptor")
		}
		if _, err := descriptor.Stat(); err == nil {
			t.Fatal("refused namespace left its held descriptor open")
		}
	})

	t.Run("nil namespace verifies as unavailable", func(t *testing.T) {
		t.Parallel()
		var nilNamespace *publicationNamespace
		if err := nilNamespace.verify(); err == nil {
			t.Fatal("nil namespace verified")
		}
		if err := (&publicationNamespace{path: t.TempDir()}).verify(); err == nil {
			t.Fatal("descriptor-less namespace verified")
		}
	})

	t.Run("nil namespace closes harmlessly", func(t *testing.T) {
		t.Parallel()
		var nilNamespace *publicationNamespace
		if err := nilNamespace.Close(); err != nil {
			t.Fatalf("nil namespace close = %v", err)
		}
		if err := (&publicationNamespace{}).Close(); err != nil {
			t.Fatalf("descriptor-less namespace close = %v", err)
		}
	})

	t.Run("initial stale parent before reader", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		path := filepath.Join(root, "evidence.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		read := false
		ops := productionSecureEvidenceReadOps()
		ops.read = func(reader io.Reader) ([]byte, error) {
			read = true
			return io.ReadAll(reader)
		}
		ops.openParent = func(directory string) (*os.File, error) {
			parent, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(directory, false)
			if err != nil {
				return nil, err
			}
			if err := os.Rename(root, root+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(root+"-held", root); err != nil {
				t.Fatal(err)
			}
			return parent, nil
		}
		if _, err := readSecureBoundedRegularFileWith(path, maxRetiredPublicationEvidenceBytes, ops); err == nil || !strings.Contains(err.Error(), "parent namespace changed") {
			t.Fatalf("initial stale parent = %v", err)
		}
		if read {
			t.Fatal("initial stale parent reached the reader")
		}
	})

	t.Run("held namespace stale before in-namespace read", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		held := filepath.Join(root, "evidence")
		if err := os.MkdirAll(held, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(held, "ack.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		namespace, err := openPublicationNamespace(path, false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = namespace.Close() }()
		read := false
		ops := productionSecureEvidenceReadOps()
		ops.read = func(reader io.Reader) ([]byte, error) {
			read = true
			return io.ReadAll(reader)
		}
		if err := os.Rename(held, held+"-held"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(held+"-held", held); err != nil {
			t.Fatal(err)
		}
		if _, err := readSecureBoundedRegularFileInNamespaceWith(path, maxRetiredPublicationEvidenceBytes, namespace, ops); err == nil || !strings.Contains(err.Error(), "no longer holds") {
			t.Fatalf("stale held namespace = %v", err)
		}
		if read {
			t.Fatal("stale held namespace reached the reader")
		}
	})
}
