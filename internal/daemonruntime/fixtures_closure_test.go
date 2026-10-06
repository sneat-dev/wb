package daemonruntime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

func daemonSupervisorTestInstalledOld(t *testing.T, root string, listen string, supervisor daemon.Supervisor, now time.Time) daemon.State {
	t.Helper()
	executable := filepath.Join(root, "wb")
	old := daemonTestState(t, root, listen, daemon.Provenance{Executable: executable, SHA256: "stale-recorded-sha-from-before-the-self-update", Version: "old"}, "owner-token", now)
	old.Supervisor = supervisor
	return old
}

func cwWtBridgeDirs(server *FileBridgeServer) (base, quarantine string) {
	base = filepath.Dir(server.requests)
	return base, filepath.Join(base, "quarantine")
}

func cwWtBridgeSigned(server *FileBridgeServer, envelope daemonFileEnvelope) daemonFileEnvelope {
	envelope.PayloadSHA256 = daemonFilePayloadDigest(envelope)
	envelope.MAC = daemonFileEnvelopeMAC(envelope, server.key)
	return envelope
}

func cwWtBridgePut(t *testing.T, server *FileBridgeServer, directory string, envelope daemonFileEnvelope) string {
	t.Helper()
	signed := cwWtBridgeSigned(server, envelope)
	if err := writeDaemonFileEnvelope(directory, signed.ID, signed); err != nil {
		t.Fatalf("cwWt: write envelope %s: %v", signed.ID, err)
	}
	return filepath.Join(directory, signed.ID+".json")
}

func cwWtBridgeResponse(t *testing.T, server *FileBridgeServer, id string) daemonFileEnvelope {
	t.Helper()
	envelope, err := readDaemonFileEnvelope(filepath.Join(server.responses, id+".json"))
	if err != nil {
		t.Fatalf("cwWt: read bridge response %s: %v", id, err)
	}
	if err := verifyDaemonFileEnvelope(envelope, server.key); err != nil {
		t.Fatalf("cwWt: verify bridge response %s: %v", id, err)
	}
	return envelope
}

func cwWtBridgeDrainError(server *FileBridgeServer) error {
	select {
	case err := <-server.errors:
		return err
	default:
		return nil
	}
}

func cwWtBridgeStat(path string) error {
	_, err := os.Stat(path)
	return err
}
