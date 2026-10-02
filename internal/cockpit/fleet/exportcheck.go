package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agentfields"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
)

// This file is the security boundary for a remote machine's export envelope
// (cockpit-views#req:remote-envelope-is-untrusted). Nothing in it trusts the
// bytes it is given:
//
//   - the body is read up to MaxEnvelopeBytes, then its shape is scanned token by
//     token, before anything is allocated for it, refusing an array over its cap,
//     nesting beyond maxDepth and more than maxTokens tokens;
//   - it is then decoded with unknown fields refused;
//   - then every field of what was decoded is walked by reflection and held to a
//     rule. A string field with no rule in stringRules is refused (the walk fails
//     closed), so a field added to the document later cannot slip through; a test
//     enumerates every string field and fails until it has one;
//   - a refusal names a rule and a field path built from the document's own field
//     names, never a value or an error text of the remote.

// The caps of the strict decoder. A string is at most maxFieldBytes bytes (an
// address maxURLLength), a collection at most maxCollection entries unless
// collectionLimits names a smaller bound, and a metrics history
// machinemetrics.Capacity samples. maxDepth is the nesting of the deepest valid
// envelope (a kind inside statistics inside a code index inside a repository
// inside the fleet) and maxTokens a bound on the JSON tokens of a body.
const (
	maxFieldBytes = agentfields.MaxTextBytes
	maxCollection = 5000
	maxCodeIndex  = 32
	maxDepth      = 10
	maxTokens     = 1_000_000
	// maxCount bounds a count or a number a document holds, and maxLoad a load average.
	maxCount = 10_000_000
	maxLoad  = 100_000
)

// collectionLimits are the bounds that differ from maxCollection, by the JSON
// name of the collection: the agents are capped at the document's own limit, an
// export has one machine, and the metrics history is one hour of samples.
var collectionLimits = map[string]int{
	"agents":     agentCap,
	"machines":   1,
	"kinds":      maxKinds,
	"code_index": maxCodeIndex,
	"samples":    machinemetrics.Capacity,
	"per_day":    ThroughputWindowDays,
	"slowest":    throughputSlowest,
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

// ReadError is a failure to read an envelope's bytes, as opposed to a refusal of
// them. Its message is fixed; Unwrap gives the cause.
type ReadError struct{ cause error }

func (e *ReadError) Error() string { return "the export envelope could not be read" }
func (e *ReadError) Unwrap() error { return e.cause }

// DecodeEnvelope reads an envelope strictly from reader: at most
// MaxEnvelopeBytes, a shape within its caps, one JSON value, no unknown field,
// then Validate. It is the only way a remote machine's envelope enters the
// daemon. metricsOnly says which of the two shapes was asked for.
func DecodeEnvelope(reader io.Reader, metricsOnly bool, now time.Time) (Envelope, error) {
	body, err := io.ReadAll(io.LimitReader(reader, MaxEnvelopeBytes+1))
	if err != nil {
		return Envelope{}, &ReadError{cause: err}
	}
	if len(body) > MaxEnvelopeBytes {
		return Envelope{}, refuse("larger than %d bytes", MaxEnvelopeBytes)
	}
	if err := scanShape(body); err != nil {
		return Envelope{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, refuse("%s", describeDecodeError(err))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Envelope{}, refuse("data after the envelope")
	}
	if err := envelope.Validate(metricsOnly, now); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// describeDecodeError is fixed text for a decode failure, chosen by the kind of
// error (the shape scan has already refused anything that is not valid JSON). The text of the error itself is never used: it quotes the remote's
// bytes (a field name, a time, a number).
func describeDecodeError(err error) string {
	var typed *json.UnmarshalTypeError
	switch {
	case errors.As(err, &typed):
		return "a field has the wrong type, at " + typed.Field
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return "an unknown field"
	}
	return "a field has an invalid value"
}

// frame is one open array or object of the shape scan.
type frame struct {
	array   bool
	key     string // the key the container sits under, which names its cap
	count   int    // elements so far, of an array
	wantKey bool   // an object expecting its next key
}

// scanShape refuses a body whose shape would cost more to decode than the
// envelope may: an array longer than its cap (by the key it sits under), nesting
// deeper than maxDepth, or more than maxTokens tokens. It allocates only what
// one token needs and runs before the decode allocates anything for the body.
func scanShape(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var stack []frame
	tokens := 0
	// finish ends a value: its parent object expects a key again. It reports
	// whether the top-level value is complete.
	finish := func() bool {
		if len(stack) == 0 {
			return true
		}
		if parent := &stack[len(stack)-1]; !parent.array {
			parent.wantKey = true
		}
		return false
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			return refuse("not valid JSON")
		}
		if tokens++; tokens > maxTokens {
			return refuse("more than %d JSON tokens", maxTokens)
		}
		var top *frame
		if len(stack) > 0 {
			top = &stack[len(stack)-1]
		}
		if top != nil && !top.array && top.wantKey {
			if delimiter, isDelimiter := token.(json.Delim); isDelimiter && delimiter == '}' {
				stack = stack[:len(stack)-1]
				if finish() {
					return nil
				}
				continue
			}
			top.key, top.wantKey = token.(string), false
			continue
		}
		if delimiter, isDelimiter := token.(json.Delim); isDelimiter && (delimiter == ']' || delimiter == '}') {
			stack = stack[:len(stack)-1]
			if finish() {
				return nil
			}
			continue
		}
		key := ""
		if top != nil {
			key = top.key
			if top.array {
				if top.count++; top.count > limitOf(top.key) {
					return refuse("a collection has more than %d entries", limitOf(top.key))
				}
			}
		}
		if delimiter, isDelimiter := token.(json.Delim); isDelimiter {
			if len(stack) == maxDepth {
				return refuse("nested deeper than %d levels", maxDepth)
			}
			stack = append(stack, frame{array: delimiter == '[', key: key, wantKey: delimiter == '{'})
			continue
		}
		if finish() {
			return nil
		}
	}
}

// limitOf is the entry cap of the collection under key.
func limitOf(key string) int {
	if bound, found := collectionLimits[key]; found {
		return bound
	}
	return maxCollection
}

// Validate refuses an envelope that breaks a rule of
// cockpit-views#req:remote-envelope-is-untrusted: the schema versions, the
// shape asked for, every field held to the rule of its kind and name (see
// checkValue), the document's identity rules and the metrics history's rules.
// It does not look at which machine the envelope names: that is never used for
// placement, and a merger re-derives every id under the configured machine's key.
func (e Envelope) Validate(metricsOnly bool, now time.Time) error {
	if e.SchemaVersion != ExportSchemaVersion {
		return refuse("schema_version is not %d", ExportSchemaVersion)
	}
	if err := checkValue(reflect.ValueOf(e), "", "", "", now); err != nil {
		return err
	}
	if e.ExportedAt.IsZero() {
		return refuse("exported_at is missing")
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
		if err := validateDocument(e.Fleet); err != nil {
			return err
		}
	}
	return validateMetrics(*e.Metrics, now)
}

// The patterns of identifiers. An id is a collection prefix and 20 hex digits
// (entryID); a token is a run or session identifier or the name of an indexer;
// a model is a token that may also hold the characters a model name uses.
var (
	idPattern      = regexp.MustCompile(`^[a-z]{2,6}-[0-9a-f]{20}$`)
	tokenPattern   = agentfields.Token
	modelPattern   = agentfields.Model
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9._+()-]{1,64}$`)
	datePattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// textRule decides whether a string field's value is acceptable.
type textRule func(string) bool

func isText(value string) bool { return agentfields.IsText(value) }

func isSafeURL(value string) bool {
	return value == "" || (len(value) <= maxURLLength && safeHTTPSURL(value) == value)
}

func isHost(value string) bool {
	return value == "" || (len(value) <= 253 && hostnamePattern.MatchString(value))
}

// matching is a rule for a field that is absent or matches pattern.
func matching(pattern *regexp.Regexp) textRule {
	return func(value string) bool { return value == "" || pattern.MatchString(value) }
}

// oneOf is a rule for a field that is absent or one of values.
func oneOf(values ...string) textRule {
	return func(value string) bool { return value == "" || slices.Contains(values, value) }
}

// required makes a rule refuse an empty value.
func required(rule textRule) textRule {
	return func(value string) bool { return value != "" && rule(value) }
}

// stringRules is the rule of every string field, by the Go name of its struct
// and its JSON name. A string field with no entry is refused.
var stringRules = map[string]textRule{
	"Envelope.machine":             isText,
	"EnvelopeMetrics.route":        required(oneOf(RouteLocal, RouteNone)),
	"EnvelopeMetrics.reason":       oneOf(ReasonNoSource, ReasonUnsupported, ReasonUnavailable, ReasonStale),
	"Document.error":               oneOf(ErrorRepositoriesUnreadable),
	"Document.code_index_provider": matching(tokenPattern),
	"Entry.id":                     required(matching(idPattern)),
	"Entry.machine":                isText,
	"Entry.machine_id":             required(matching(idPattern)),
	"Entry.route":                  required(oneOf(RouteLocal)),
	"Machine.wb_version":           matching(versionPattern),
	"Machine.os":                   matching(shortNamePattern),
	"Machine.arch":                 matching(shortNamePattern),
	// An export is a machine's own entry, which never carries the transport or
	// the error of a read of another machine: both must be absent.
	"Machine.transport":    oneOf(),
	"Machine.remote_error": oneOf(),
	// The code of this machine's own last failed publish is local only: an export
	// never carries it (NewEnvelope clears it), and one that does is refused.
	"Machine.publish_error":     oneOf(),
	"Repository.host":           isHost,
	"Repository.name":           isText,
	"Repository.default_branch": isText,
	"Repository.error":          oneOf(ErrorTimeout, ErrorReadFailed),
	"Repository.remote_url_web": isSafeURL,
	"Worktree.repository":       matching(idPattern),
	"Worktree.name":             isText,
	"Worktree.task":             isText,
	"Worktree.stream":           isText,
	"Worktree.branch":           isText,
	"Worktree.lifecycle":        oneOf(remoteLifecycles...),
	"Worktree.owner_state":      oneOf(OwnerActive, OwnerIdle, OwnerOrphaned, OwnerUnknown),
	"PullRequest.repository":    matching(idPattern),
	"PullRequest.worktree":      matching(idPattern),
	"PullRequest.branch":        isText,
	"PullRequest.state":         oneOf("open", "merged", "closed", "draft"),
	"PullRequest.url":           isSafeURL,
	"PullRequest.mergeable":     oneOf("clean", "blocked", "dirty", "behind", "unstable", "has_hooks", "draft", "unknown"),
	"PullRequest.failed_check":  isText,
	"Agent.kind":                required(oneOf(AgentSession, AgentRun)),
	"Agent.session_id":          matching(tokenPattern),
	"Agent.run_id":              matching(tokenPattern),
	"Agent.runtime":             matching(tokenPattern),
	"Agent.model":               matching(modelPattern),
	"Agent.state":               required(oneOf("live", "parked", "running", "completed", "failed", "timeout", "abandoned")),
	"Agent.activity":            oneOf(ActivityWorking, ActivityBlocked, ActivityIdle, ActivityDone, ActivityUnknown),
	"Agent.repository":          matching(idPattern),
	"Agent.task":                isText,
	"Agent.worktrees":           matching(idPattern),
	"CodeIndex.indexer":         required(matching(tokenPattern)),
	"CodeIndex.state":           required(oneOf(CodeIndexFresh, CodeIndexStale, CodeIndexDiverged, CodeIndexPending, CodeIndexFailed, CodeIndexNever)),
	"CodeStatistics.error":      oneOf(ErrorProviderUnavailable, ErrorProviderTimeout, ErrorProviderFailed, ErrorProviderOutput),
	"KindCount.kind":            required(matching(kindPattern)),
	"ThroughputDay.date":        required(matching(datePattern)),
	"ThroughputTask.task":       required(isText),
}

// rulesForUnmergedFields are rules named in stringRules for fields that are not
// in this tree's types yet. It is empty now that the pull request state fields
// have merged; a later task that pre-registers a rule lists its field here.
var rulesForUnmergedFields = map[string]bool{}

var (
	timeType        = reflect.TypeFor[time.Time]()
	unmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[interface{ UnmarshalText([]byte) error }]()
)

// plausibleTime reports whether when is unset, or after the earliest time a
// machine may report (the boot-time rule) and not in the future.
func plausibleTime(when, now time.Time) bool {
	return when.IsZero() || (!when.Before(earliestBootTime) && !when.After(now.Add(maxSkew)))
}

// checkValue holds value, reached by path and named name in JSON as a field of
// the struct owner, to the rules of its kind. A type that decodes itself (other
// than time.Time), a byte slice, a map, an interface or any other kind is
// refused: only the plain kinds of the document types are allowed.
func checkValue(value reflect.Value, path, owner, name string, now time.Time) error {
	if value.Type() != timeType && (reflect.PointerTo(value.Type()).Implements(unmarshalerType) || reflect.PointerTo(value.Type()).Implements(textUnmarshaler)) {
		return refuse("%s has a type that decodes itself", path)
	}
	switch value.Kind() {
	case reflect.String:
		rule, found := stringRules[owner+"."+name]
		if !found {
			return refuse("%s has no rule", path)
		}
		if !rule(value.String()) {
			return refuse("%s is not valid", path)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Int() < 0 || value.Int() > maxCount {
			return refuse("%s is negative or too large", path)
		}
	case reflect.Float32, reflect.Float64:
		if number := value.Float(); math.IsNaN(number) || number < 0 || number > maxCount*100 {
			return refuse("%s is not a finite, non-negative number within range", path)
		}
	case reflect.Bool, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
	case reflect.Pointer:
		if !value.IsNil() {
			return checkValue(value.Elem(), path, owner, name, now)
		}
	case reflect.Slice:
		return checkSlice(value, path, owner, name, now)
	case reflect.Struct:
		return checkStruct(value, path, now)
	default:
		return refuse("%s has a kind the envelope does not allow", path)
	}
	return nil
}

// checkSlice holds a list to its cap, refuses a byte slice, and checks each
// element.
func checkSlice(value reflect.Value, path, owner, name string, now time.Time) error {
	if value.Type().Elem().Kind() == reflect.Uint8 {
		return refuse("%s is a byte slice, which the envelope does not allow", path)
	}
	if value.Len() > limitOf(name) {
		return refuse("%s has more than %d entries", path, limitOf(name))
	}
	for index := range value.Len() {
		if err := checkValue(value.Index(index), fmt.Sprintf("%s[%d]", path, index), owner, name, now); err != nil {
			return err
		}
	}
	return nil
}

// checkStruct holds a time to its range and walks the exported fields of any
// other struct, an embedded one as part of its parent.
func checkStruct(value reflect.Value, path string, now time.Time) error {
	if value.Type() == timeType {
		if !plausibleTime(value.Interface().(time.Time), now) {
			return refuse("%s is before 2000 or in the future", path)
		}
		return nil
	}
	typeName := value.Type().Name()
	for index := range value.NumField() {
		field := value.Type().Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		child := value.Field(index)
		switch {
		case !field.IsExported() || name == "-":
			continue
		case field.Anonymous && name == "":
			if child.Kind() == reflect.Pointer {
				if child.IsNil() {
					continue
				}
				child = child.Elem()
			}
			if err := checkStruct(child, path, now); err != nil {
				return err
			}
			continue
		case name == "":
			name = field.Name
		}
		if err := checkValue(child, joinPath(path, name), typeName, name, now); err != nil {
			return err
		}
	}
	return nil
}

// joinPath extends a field path with a field name.
func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// validateDocument holds the document's identity rules: no collection is null
// (the document never has a null list), the export has at most one machine
// entry, and when it has entries every entry belongs to that machine, carries
// an id of its own that is unique in the envelope and is a route `local` entry
// (checked by the field rule), a repository's web address is the one built from
// its host and name, and a pull request's address is on its repository's host.
func validateDocument(document *Document) error {
	if document.SchemaVersion != SchemaVersion {
		return refuse("fleet.schema_version is not %d", SchemaVersion)
	}
	if document.Throughput != nil {
		return refuse("fleet.throughput is local only and never exported")
	}
	if document.Machines == nil || document.Repositories == nil || document.Worktrees == nil || document.PullRequests == nil || document.Agents == nil {
		return refuse("fleet has a null collection")
	}
	if len(document.Machines) == 0 && len(document.Repositories)+len(document.Worktrees)+len(document.PullRequests)+len(document.Agents) > 0 {
		return refuse("fleet has entries and no machine")
	}
	machineID := ""
	if len(document.Machines) == 1 {
		machine := document.Machines[0]
		if machine.ID != machine.MachineID {
			return refuse("fleet.machines[0].machine_id is not its id")
		}
		// What a reader records about its read of another machine is never part of
		// a machine's own export.
		if machine.ExportDropped != 0 || machine.AgentsTruncated {
			return refuse("fleet.machines[0] carries a field only a reader sets")
		}
		machineID = machine.ID
	}
	type located struct {
		path  string
		index int
		entry Entry
	}
	var entries []located
	hostOf := map[string]string{}
	for index, machine := range document.Machines {
		entries = append(entries, located{"fleet.machines", index, machine.Entry})
	}
	for index, repository := range document.Repositories {
		entries = append(entries, located{"fleet.repositories", index, repository.Entry})
		if repository.RemoteURLWeb != "" && repository.RemoteURLWeb != webURL(repository.Host, repository.Name) {
			return refuse("fleet.repositories[%d].remote_url_web is not built from its host and name", index)
		}
		hostOf[repository.ID] = repository.Host
	}
	for index, worktree := range document.Worktrees {
		entries = append(entries, located{"fleet.worktrees", index, worktree.Entry})
	}
	for index, pull := range document.PullRequests {
		entries = append(entries, located{"fleet.pull_requests", index, pull.Entry})
		if host := hostOf[pull.Repository]; host != "" && pull.URL != "" && !strings.EqualFold(urlHost(pull.URL), host) {
			return refuse("fleet.pull_requests[%d].url is not on its repository's host", index)
		}
	}
	for index, agent := range document.Agents {
		entries = append(entries, located{"fleet.agents", index, agent.Entry})
		if len(agent.Worktrees) > maxAgentWorktrees {
			return refuse("fleet.agents[%d].worktrees has more than %d entries", index, maxAgentWorktrees)
		}
	}
	seen := map[string]bool{}
	for _, item := range entries {
		switch {
		case seen[item.entry.ID]:
			return refuse("%s[%d].id is not unique", item.path, item.index)
		case item.entry.MachineID != machineID:
			return refuse("%s[%d].machine_id is not the machine's", item.path, item.index)
		}
		seen[item.entry.ID] = true
	}
	for index, repository := range document.Repositories {
		if nullKinds(repository.CodeIndex) {
			return refuse("fleet.repositories[%d] statistics has a null kinds list", index)
		}
	}
	for index, worktree := range document.Worktrees {
		if nullKinds(worktree.CodeIndex) {
			return refuse("fleet.worktrees[%d] statistics has a null kinds list", index)
		}
	}
	return nil
}

// urlHost is the host of an address that passed safeHTTPSURL.
func urlHost(address string) string {
	parsed, _ := url.Parse(address)
	return parsed.Host
}

// nullKinds reports whether a code index carries statistics whose kinds list is
// null, which the document never has.
func nullKinds(indexes []CodeIndex) bool {
	return slices.ContainsFunc(indexes, func(indexed CodeIndex) bool { return indexed.Statistics != nil && indexed.Statistics.Kinds == nil })
}

// validateMetrics holds an envelope's metrics to the rules of the metrics
// payload that the field walk does not know: a `none` has a reason and no
// samples, a history has no reason and a null list is refused, and the samples
// have times that strictly increase, a percent within 0 to 100, a load within
// range, and each pair of figures present together with the used or free one not
// above its total. The walk has already held every number to its range and every
// time to be plausible.
func validateMetrics(metrics EnvelopeMetrics, now time.Time) error {
	if metrics.Samples == nil {
		return refuse("metrics.samples is null")
	}
	switch metrics.Route {
	case RouteLocal:
		if metrics.Reason != "" {
			return refuse("metrics.reason is set on a history")
		}
	case RouteNone:
		if metrics.Reason == "" || len(metrics.Samples) > 0 {
			return refuse("metrics.route none needs a reason and no samples")
		}
	}
	var last time.Time
	for index, sample := range metrics.Samples {
		path := fmt.Sprintf("metrics.samples[%d]", index)
		if sample.SampledAt.IsZero() || !sample.SampledAt.After(last) {
			return refuse("%s.sampled_at is missing or not in increasing order", path)
		}
		last = sample.SampledAt
		if err := validateSample(sample, path); err != nil {
			return err
		}
	}
	return nil
}

// validateSample holds the measurements of one sample to their relations.
func validateSample(sample machinemetrics.Sample, path string) error {
	if sample.CPUPercent != nil && *sample.CPUPercent > 100 {
		return refuse("%s.cpu_percent is over 100", path)
	}
	if sample.Load1 != nil && *sample.Load1 > maxLoad {
		return refuse("%s.load1 is out of range", path)
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
