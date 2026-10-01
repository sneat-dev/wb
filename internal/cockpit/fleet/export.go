package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
)

// The export envelope is what one machine tells another about itself
// (cockpit-views#req:cockpit-export-verb): this machine's own entries of the
// fleet document, which the anonymous-local principal may read, and its
// machine-metrics history. It travels over the loopback transport of the CLI
// verb, over the hub route and over SSH, and the same strict decoder in this
// file is the security boundary for every one of them
// (cockpit-views#req:remote-envelope-is-untrusted): what a remote machine
// sends is data, never trusted, never applied to this machine.

// ExportSchemaVersion is the envelope format this binary writes and reads.
const ExportSchemaVersion = 1

// MaxEnvelopeBytes bounds an envelope as printed and as read.
const MaxEnvelopeBytes = 8 << 20

// The two error codes of an export that could not be made, printed as an
// ExportError.
const (
	ErrorDaemonNotRunning = "daemon_not_running"
	ErrorExportRefused    = "export_refused"
)

// The caps of the strict decoder. A string is at most maxFieldBytes bytes (an
// address maxURLLength), a collection at most maxCollection entries unless
// collectionLimits names a smaller bound, and a metrics history
// machinemetrics.Capacity samples.
const (
	maxFieldBytes = 256
	maxCollection = 5000
	maxMachines   = 64
	maxCodeIndex  = 32
)

// collectionLimits are the bounds that differ from maxCollection, by the JSON
// name of the collection: the agents are capped at the document's own limit.
var collectionLimits = map[string]int{
	"agents":     agentCap,
	"machines":   maxMachines,
	"kinds":      maxKinds,
	"code_index": maxCodeIndex,
}

// addressFields are the JSON names of the fields that hold an address.
var addressFields = map[string]bool{"url": true, "remote_url_web": true}

// Envelope is the export envelope. Fleet is absent in a metrics-only export.
type Envelope struct {
	SchemaVersion int `json:"schema_version"`
	// Machine is the exporting machine's name. It is informational: no reader
	// places an entry by it.
	Machine    string           `json:"machine"`
	ExportedAt time.Time        `json:"exported_at"`
	Fleet      *Document        `json:"fleet,omitempty"`
	Metrics    *EnvelopeMetrics `json:"metrics,omitempty"`
}

// EnvelopeMetrics is the exporting machine's own metrics: the route it came
// by (`local`, or `none` with a Reason) and the history, oldest first, whose
// last element is the latest sample.
type EnvelopeMetrics struct {
	Route   string                  `json:"route"`
	Samples []machinemetrics.Sample `json:"samples"`
	Reason  string                  `json:"reason,omitempty"`
}

// ExportError is what the export verb prints when it could not make an
// envelope.
type ExportError struct {
	SchemaVersion int    `json:"schema_version"`
	Error         string `json:"error"`
}

// NewExportError is the ExportError for code.
func NewExportError(code string) ExportError {
	return ExportError{SchemaVersion: ExportSchemaVersion, Error: code}
}

// NewEnvelope is the one function that builds an envelope, for the CLI verb
// (from what the daemon's routes served) and for the hub route (from the
// daemon's own state in process). It keeps this machine's own entries only:
// an entry that is cached from another machine, or live from one, is never
// re-exported. A metrics-only envelope has no fleet.
func NewEnvelope(document Document, metrics MetricsResponse, now time.Time, metricsOnly bool) Envelope {
	envelope := Envelope{SchemaVersion: ExportSchemaVersion, ExportedAt: now.UTC(), Metrics: exportMetrics(metrics)}
	for _, machine := range document.Machines {
		if machine.Route == RouteLocal {
			envelope.Machine = machine.Machine
			break
		}
	}
	if !metricsOnly {
		own := document
		own.Machines = keepLocal(document.Machines, func(machine Machine) Entry { return machine.Entry })
		own.Repositories = keepLocal(document.Repositories, func(repository Repository) Entry { return repository.Entry })
		own.Worktrees = keepLocal(document.Worktrees, func(worktree Worktree) Entry { return worktree.Entry })
		own.PullRequests = keepLocal(document.PullRequests, func(pull PullRequest) Entry { return pull.Entry })
		own.Agents = keepLocal(document.Agents, func(agent Agent) Entry { return agent.Entry })
		envelope.Fleet = &own
	}
	return envelope
}

// keepLocal is the entries of list that this machine observed itself, as a
// list that is never nil.
func keepLocal[T any](list []T, entry func(T) Entry) []T {
	kept := make([]T, 0, len(list))
	for _, item := range list {
		if entry(item).Route == RouteLocal {
			kept = append(kept, item)
		}
	}
	return kept
}

// exportMetrics maps the machine-metrics answer for this machine: only its
// own history (`local`) is exported, and any other answer is `none`.
func exportMetrics(response MetricsResponse) *EnvelopeMetrics {
	switch response.Route {
	case RouteLocal:
		samples := response.Samples
		if samples == nil {
			samples = []machinemetrics.Sample{}
		}
		return &EnvelopeMetrics{Route: RouteLocal, Samples: samples}
	case RouteNone:
		if slices.Contains([]string{ReasonNoSource, ReasonUnsupported, ReasonUnavailable}, response.Reason) {
			return &EnvelopeMetrics{Route: RouteNone, Samples: []machinemetrics.Sample{}, Reason: response.Reason}
		}
	}
	return &EnvelopeMetrics{Route: RouteNone, Samples: []machinemetrics.Sample{}, Reason: ReasonUnavailable}
}

// BadEnvelopeError is a refused envelope. Reason names the rule and the
// field's path, from fixed text and the document's own field names: it never
// holds the remote's text, so it is safe to log and never to be shown as the
// remote's words.
type BadEnvelopeError struct{ Reason string }

func (e *BadEnvelopeError) Error() string { return "export envelope refused: " + e.Reason }

func refuse(format string, args ...any) error {
	return &BadEnvelopeError{Reason: fmt.Sprintf(format, args...)}
}

// DecodeEnvelope reads an envelope strictly from reader: at most
// MaxEnvelopeBytes, one JSON value, no unknown field, then Validate. It is the
// only way a remote machine's envelope enters the daemon. metricsOnly says
// which of the two shapes was asked for.
func DecodeEnvelope(reader io.Reader, metricsOnly bool, now time.Time) (Envelope, error) {
	body, err := io.ReadAll(io.LimitReader(reader, MaxEnvelopeBytes+1))
	if err != nil {
		return Envelope{}, fmt.Errorf("read the export envelope: %w", err)
	}
	if len(body) > MaxEnvelopeBytes {
		return Envelope{}, refuse("larger than %d bytes", MaxEnvelopeBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, refuse("not an envelope: %s", plainText(err.Error()))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Envelope{}, refuse("data after the envelope")
	}
	if err := envelope.Validate(metricsOnly, now); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// Validate refuses an envelope that breaks a rule of
// cockpit-views#req:remote-envelope-is-untrusted: the schema versions, the
// shape asked for, every string length-capped and free of control characters,
// every address a safe https one, every collection capped, no negative or
// non-finite number, no time in the future, the vocabularies closed, and the
// metrics history within its own rules. It does not look at which machine the
// envelope names: that is never used for placement.
func (e Envelope) Validate(metricsOnly bool, now time.Time) error {
	if e.SchemaVersion != ExportSchemaVersion {
		return refuse("schema_version is not %d", ExportSchemaVersion)
	}
	if err := checkString(e.Machine, "machine", "machine"); err != nil {
		return err
	}
	if e.ExportedAt.IsZero() || e.ExportedAt.After(now.Add(maxSkew)) {
		return refuse("exported_at is missing or in the future")
	}
	switch {
	case metricsOnly && e.Fleet != nil:
		return refuse("a metrics-only export carries a fleet")
	case !metricsOnly && e.Fleet == nil:
		return refuse("the export has no fleet")
	case e.Metrics == nil:
		return refuse("the export has no metrics")
	}
	if e.Fleet != nil {
		if err := validateDocument(e.Fleet, now); err != nil {
			return err
		}
	}
	return validateMetrics(*e.Metrics, now)
}

var timeType = reflect.TypeFor[time.Time]()

// validateDocument walks every field of the document by reflection, so a field
// the document gains later is held to the same caps without a list to update,
// then checks the closed vocabularies.
func validateDocument(document *Document, now time.Time) error {
	if document.SchemaVersion != SchemaVersion {
		return refuse("fleet.schema_version is not %d", SchemaVersion)
	}
	if err := checkValue(reflect.ValueOf(document).Elem(), "fleet", "", now); err != nil {
		return err
	}
	routes := []string{RouteLocal, RouteCached, RouteLiveRemote}
	check := func(path string, index int, entry Entry) error {
		if !slices.Contains(routes, entry.Route) {
			return refuse("%s[%d].route is not a route", path, index)
		}
		return nil
	}
	for index, machine := range document.Machines {
		if err := check("fleet.machines", index, machine.Entry); err != nil {
			return err
		}
	}
	for index, repository := range document.Repositories {
		if err := check("fleet.repositories", index, repository.Entry); err != nil {
			return err
		}
	}
	for index, worktree := range document.Worktrees {
		if err := check("fleet.worktrees", index, worktree.Entry); err != nil {
			return err
		}
		if worktree.OwnerState != "" && publishedOwnerState(worktree.OwnerState) == "" {
			return refuse("fleet.worktrees[%d].owner_state is not an owner state", index)
		}
	}
	for index, pull := range document.PullRequests {
		if err := check("fleet.pull_requests", index, pull.Entry); err != nil {
			return err
		}
	}
	for index, agent := range document.Agents {
		if err := check("fleet.agents", index, agent.Entry); err != nil {
			return err
		}
		if agent.Kind != AgentSession && agent.Kind != AgentRun {
			return refuse("fleet.agents[%d].kind is not an agent kind", index)
		}
	}
	return nil
}

// checkString holds one string to its cap, its encoding and the plain-text
// rule of plainText (no control, format or separator character), and an
// address to the safe https shape.
func checkString(text, path, name string) error {
	limit := maxFieldBytes
	if addressFields[name] {
		limit = maxURLLength
	}
	switch {
	case len(text) > limit:
		return refuse("%s is longer than %d bytes", path, limit)
	case !utf8.ValidString(text) || strings.ContainsFunc(text, unsafeRune):
		return refuse("%s is not plain text", path)
	case addressFields[name] && text != "" && safeHTTPSURL(text) != text:
		return refuse("%s is not a safe https address", path)
	}
	return nil
}

// checkValue holds value, reached by path and named name in JSON, to the rules
// of its kind.
func checkValue(value reflect.Value, path, name string, now time.Time) error {
	switch value.Kind() {
	case reflect.String:
		return checkString(value.String(), path, name)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Int() < 0 {
			return refuse("%s is negative", path)
		}
	case reflect.Float32, reflect.Float64:
		if number := value.Float(); math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return refuse("%s is not a finite, non-negative number", path)
		}
	case reflect.Bool, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
	case reflect.Pointer:
		if !value.IsNil() {
			return checkValue(value.Elem(), path, name, now)
		}
	case reflect.Slice:
		limit := maxCollection
		if bound, found := collectionLimits[name]; found {
			limit = bound
		}
		if value.Len() > limit {
			return refuse("%s has more than %d entries", path, limit)
		}
		for index := range value.Len() {
			if err := checkValue(value.Index(index), fmt.Sprintf("%s[%d]", path, index), name, now); err != nil {
				return err
			}
		}
	case reflect.Struct:
		return checkStruct(value, path, now)
	default:
		return refuse("%s has a kind the envelope does not allow", path)
	}
	return nil
}

// checkStruct holds a time not to be in the future and walks the exported
// fields of any other struct, an embedded one as part of its parent.
func checkStruct(value reflect.Value, path string, now time.Time) error {
	if value.Type() == timeType {
		if value.Interface().(time.Time).After(now.Add(maxSkew)) {
			return refuse("%s is in the future", path)
		}
		return nil
	}
	for index := range value.NumField() {
		field := value.Type().Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch {
		case !field.IsExported() || name == "-":
			continue
		case field.Anonymous && name == "":
			if err := checkStruct(value.Field(index), path, now); err != nil {
				return err
			}
			continue
		case name == "":
			name = field.Name
		}
		if err := checkValue(value.Field(index), path+"."+name, name, now); err != nil {
			return err
		}
	}
	return nil
}

// validateMetrics holds an envelope's metrics to the rules of the metrics
// payload: the route is `local` or `none`, a `none` has a reason of the closed
// set and no samples, and a history has at most 360 samples, each with its
// time set, not in the future and strictly later than the one before, a
// percent within 0 to 100, no negative or non-finite number, and each pair of
// figures present together with the used or free one not above its total.
func validateMetrics(metrics EnvelopeMetrics, now time.Time) error {
	switch metrics.Route {
	case RouteLocal:
		if metrics.Reason != "" {
			return refuse("metrics.reason is set on a history")
		}
	case RouteNone:
		if !slices.Contains([]string{ReasonNoSource, ReasonUnsupported, ReasonUnavailable}, metrics.Reason) || len(metrics.Samples) > 0 {
			return refuse("metrics.route none needs a reason and no samples")
		}
	default:
		return refuse("metrics.route is not local or none")
	}
	if len(metrics.Samples) > machinemetrics.Capacity {
		return refuse("metrics.samples has more than %d samples", machinemetrics.Capacity)
	}
	var last time.Time
	for index, sample := range metrics.Samples {
		path := fmt.Sprintf("metrics.samples[%d]", index)
		if sample.SampledAt.IsZero() || sample.SampledAt.After(now.Add(maxSkew)) || !sample.SampledAt.After(last) {
			return refuse("%s.sampled_at is missing, in the future or not in increasing order", path)
		}
		last = sample.SampledAt
		if err := validateSample(sample, path); err != nil {
			return err
		}
	}
	return nil
}

// validateSample holds the measurements of one sample to their ranges.
func validateSample(sample machinemetrics.Sample, path string) error {
	if sample.CPUPercent != nil && !(*sample.CPUPercent >= 0 && *sample.CPUPercent <= 100) {
		return refuse("%s.cpu_percent is outside 0 to 100", path)
	}
	if sample.Load1 != nil && !(*sample.Load1 >= 0 && !math.IsInf(*sample.Load1, 0)) {
		return refuse("%s.load1 is not a finite, non-negative number", path)
	}
	if !pairWithin(sample.MemoryUsedBytes, sample.MemoryTotalBytes) {
		return refuse("%s memory figures are not a pair with used within total", path)
	}
	if !pairWithin(sample.DiskFreeBytes, sample.DiskTotalBytes) {
		return refuse("%s disk figures are not a pair with free within total", path)
	}
	return nil
}

// pairWithin reports whether part and total are both absent, or both present
// with part not above total.
func pairWithin(part, total *uint64) bool {
	if part == nil || total == nil {
		return part == nil && total == nil
	}
	return *part <= *total
}
