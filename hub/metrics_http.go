package hub

import (
	"net/http"
	"strings"
)

func (h apiHandler) listMetricTypes(w http.ResponseWriter, r *http.Request) {
	if !h.authorizedForCoverage(r) {
		writeError(w, http.StatusUnauthorized, "metrics_unauthorized")
		return
	}
	if h.options.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics_unavailable")
		return
	}
	types, err := h.options.Metrics.ListMetricTypes(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "metrics_failed")
		return
	}
	if types == nil {
		types = []MetricTypeDefinition{}
	}
	writeJSON(w, http.StatusOK, types)
}

func (h apiHandler) listMetrics(w http.ResponseWriter, r *http.Request) {
	if !h.authorizedForCoverage(r) {
		writeError(w, http.StatusUnauthorized, "metrics_unauthorized")
		return
	}
	if h.options.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics_unavailable")
		return
	}
	metricType := r.URL.Query().Get("type")
	records, err := h.options.Metrics.ListMetrics(r.Context(), metricType)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "metrics_failed")
		return
	}
	if records == nil {
		records = []RepositoryMetric{}
	}
	writeJSON(w, http.StatusOK, records)
}

func (h apiHandler) getMetric(w http.ResponseWriter, r *http.Request) {
	if !h.authorizedForCoverage(r) {
		writeError(w, http.StatusUnauthorized, "metrics_unauthorized")
		return
	}
	if h.options.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics_unavailable")
		return
	}
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	target := owner + "/" + repo
	if owner == "" || repo == "" {
		target = strings.TrimPrefix(r.URL.Path, MetricsPath+"/")
	}
	metricType := r.URL.Query().Get("type")
	record, found, err := h.options.Metrics.GetMetric(r.Context(), target, metricType)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "metrics_failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "metric_not_found")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (h apiHandler) saveMetric(w http.ResponseWriter, r *http.Request) {
	if !h.authorizedForCoverage(r) {
		writeError(w, http.StatusUnauthorized, "metrics_unauthorized")
		return
	}
	if h.options.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics_unavailable")
		return
	}
	var record RepositoryMetric
	if err := decodeRequest(w, r, maxJSONBodyBytes, &record); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_metric_payload")
		return
	}
	if err := h.options.Metrics.SaveMetric(r.Context(), record); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, record)
}
