package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// redactionMarker replaces an injected credential wherever it appears in a
// captured stream.
const redactionMarker = "[redacted]"

// TaskIdentity returns the identity of the exact task bytes without keeping a
// second copy of them.
func TaskIdentity(task string) (digest string, size int) {
	sum := sha256.Sum256([]byte(task))
	return hex.EncodeToString(sum[:]), len(task)
}

// redactingWriter removes an injected credential from a stream before it reaches
// disk. It holds back the last len(secret)-1 bytes of every write, because a
// secret can be split across two writes and must still be found.
type redactingWriter struct {
	inner  io.Writer
	secret []byte
	tail   []byte
	// replaced counts removals, so the run can record that redaction happened
	// without recording what was removed.
	replaced int
}

func newRedactingWriter(inner io.Writer, secret string) *redactingWriter {
	if strings.TrimSpace(secret) == "" {
		return nil
	}
	return &redactingWriter{inner: inner, secret: []byte(secret)}
}

func (writer *redactingWriter) Write(data []byte) (int, error) {
	if writer == nil || len(writer.secret) == 0 {
		return writer.inner.Write(data)
	}
	buffer := append(writer.tail, data...)
	writer.tail = nil
	// Hold back the longest suffix that is a prefix of the secret: that is the
	// only part that could still become a match once the next write arrives.
	// Everything before it either contains complete matches (replaced) or no
	// match at all, so no occurrence can straddle the boundary.
	safe := len(buffer)
	for overlap := len(writer.secret) - 1; overlap >= 1; overlap-- {
		if overlap > len(buffer) {
			continue
		}
		if bytes.HasPrefix(writer.secret, buffer[len(buffer)-overlap:]) {
			safe = len(buffer) - overlap
			break
		}
	}
	writer.tail = append(writer.tail, buffer[safe:]...)
	return len(data), writer.emit(buffer[:safe])
}

func (writer *redactingWriter) emit(data []byte) error {
	count := bytes.Count(data, writer.secret)
	if count == 0 {
		_, err := writer.inner.Write(data)
		return err
	}
	writer.replaced += count
	redacted := bytes.ReplaceAll(data, writer.secret, []byte(redactionMarker))
	_, err := writer.inner.Write(redacted)
	return err
}

// Flush writes the held-back tail. It must be called before the stream is
// summarised or the file is closed.
func (writer *redactingWriter) Flush() error {
	if writer == nil || len(writer.tail) == 0 {
		return nil
	}
	tail := writer.tail
	writer.tail = nil
	return writer.emit(tail)
}

// RedactText applies the same rule to a whole value, such as the harness's own
// final-message file, which it writes itself and which the stream writer never
// sees.
func RedactText(text, secret string) (string, int) {
	if strings.TrimSpace(secret) == "" {
		return text, 0
	}
	replaced := strings.Count(text, secret)
	return strings.ReplaceAll(text, secret, redactionMarker), replaced
}

// buildTelemetry projects a harness summary into the durable telemetry shape.
// A summary without usage is reported as unavailable with the harness's own
// reason, never as zeroes.
func buildTelemetry(harness string, summary HarnessSummary) Telemetry {
	if summary.Tokens == nil {
		reason := "the harness reported no usage"
		if HarnessCapabilityFor(harness) == EffortUnsupported && harness == HarnessDSH {
			reason = "the dsh harness prints only its final message; it reports no usage"
		}
		return Telemetry{Available: false, Source: UsageUnavailable, UnavailableReason: reason}
	}
	raw, _ := json.Marshal(summary.Tokens)
	return Telemetry{Available: true, Source: UsageFromEvents, Tokens: summary.Tokens, Raw: raw}
}

// buildCost projects a harness-reported cost into the durable cost shape. A
// zero with tokens above zero is a subscription plan, not a free run.
func buildCost(harness string, summary HarnessSummary, now time.Time) CostRecord {
	if summary.CostUSD == nil {
		note := summary.CostNote
		if note == "" {
			note = "the harness reports no per-run cost"
		}
		return CostRecord{Kind: "api_equivalent", Source: CostUnavailable, Note: note}
	}
	value := *summary.CostUSD
	estimatedAt := now.UTC()
	return CostRecord{
		USD: &value, Kind: "api_equivalent", Source: CostHarnessEstimated,
		PricingVersion: harness + " price catalogue", EstimatedAt: &estimatedAt,
		Note: "estimated by the harness from its own price catalogue; not a billed amount",
	}
}

// legacyUsage projects observed tokens onto the pre-existing Usage shape so an
// older WB still sees the numbers it looks for. It is only filled from observed
// values; absent fields stay absent.
func legacyUsage(tokens *Tokens) *Usage {
	if tokens == nil {
		return nil
	}
	usage := Usage{}
	filled := false
	if tokens.Input != nil {
		usage.InputTokens, filled = *tokens.Input, true
	}
	if tokens.Output != nil {
		usage.OutputTokens, filled = *tokens.Output, true
	}
	if tokens.CacheRead != nil {
		usage.CachedInputTokens = *tokens.CacheRead
	}
	if tokens.CacheWrite != nil {
		usage.CacheWriteInputTokens = *tokens.CacheWrite
	}
	if tokens.Reasoning != nil {
		usage.ReasoningOutputTokens = *tokens.Reasoning
	}
	if !filled {
		return nil
	}
	return &usage
}
