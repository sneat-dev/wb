package githubapp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

const maxMachineSnapshotBodyBytes = 1 << 20

func (handler apiHandler) publisher(request *http.Request) (MachinePublisher, error) {
	if handler.options.PublisherResolver == nil {
		return MachinePublisher{}, ErrPublisherIdentity
	}
	return handler.options.PublisherResolver.Publisher(request)
}

func (handler apiHandler) publishMachineSnapshot(writer http.ResponseWriter, request *http.Request) {
	publisher, err := handler.publisher(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, "publisher_unavailable")
		return
	}
	if handler.options.MachineSnapshots == nil {
		writeError(writer, http.StatusServiceUnavailable, "snapshot_store_not_configured")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxMachineSnapshotBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var snapshot machinesnapshot.Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, "snapshot_payload_too_large")
			return
		}
		writeError(writer, http.StatusBadRequest, "invalid_snapshot_payload")
		return
	}
	if err := requireJSONEnd(decoder); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_snapshot_payload")
		return
	}
	receipt, err := handler.options.MachineSnapshots.Publish(request.Context(), publisher, snapshot)
	switch {
	case err == nil:
		writeJSON(writer, http.StatusOK, receipt)
	case errors.Is(err, ErrPublisherIdentity):
		writeError(writer, http.StatusUnauthorized, "publisher_unavailable")
	case errors.Is(err, ErrPublisherMismatch):
		writeError(writer, http.StatusForbidden, "publisher_identity_mismatch")
	case errors.Is(err, machinesnapshot.ErrInvalidSnapshot):
		writeError(writer, http.StatusBadRequest, "invalid_snapshot_payload")
	case errors.Is(err, machinesnapshot.ErrStaleSnapshot), errors.Is(err, machinesnapshot.ErrSnapshotConflict):
		writeError(writer, http.StatusConflict, "stale_snapshot")
	case errors.Is(err, ErrNoReadModel):
		writeError(writer, http.StatusServiceUnavailable, "snapshot_store_not_configured")
	default:
		writeError(writer, http.StatusInternalServerError, "snapshot_store_error")
	}
}

func (handler apiHandler) machineSnapshots(writer http.ResponseWriter, request *http.Request) {
	publisher, err := handler.publisher(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, "publisher_unavailable")
		return
	}
	if handler.options.MachineSnapshots == nil {
		writeError(writer, http.StatusServiceUnavailable, "snapshot_store_not_configured")
		return
	}
	response, err := handler.options.MachineSnapshots.List(request.Context(), publisher)
	if err != nil {
		if errors.Is(err, ErrPublisherIdentity) {
			writeError(writer, http.StatusUnauthorized, "publisher_unavailable")
			return
		}
		writeError(writer, http.StatusInternalServerError, "snapshot_store_error")
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func requireJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
