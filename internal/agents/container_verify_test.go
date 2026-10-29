package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

func containerInspectJSON(t *testing.T, images ...dockerImageInspect) []byte {
	t.Helper()
	payload, err := json.Marshal(images)
	if err != nil {
		t.Fatalf("marshal inspect fixtures: %v", err)
	}
	return payload
}

func TestVerifyContainerImageAcceptsOnlyTheRequestedImmutableIdentity(t *testing.T) {
	t.Parallel()
	bareID := "sha256:" + strings.Repeat("c", 64)
	configID := "sha256:" + strings.Repeat("d", 64)
	cases := map[string]struct {
		policy  ExecutionPolicy
		inspect []byte
		wantID  string
	}{
		"bare image ID must equal the config ID": {
			policy:  func() ExecutionPolicy { policy := executionTestPolicy(); policy.Image = bareID; return policy }(),
			inspect: containerInspectJSON(t, dockerImageInspect{ID: bareID, OS: "linux"}),
			wantID:  bareID,
		},
		"digest reference must appear in RepoDigests": {
			policy: executionTestPolicy(),
			inspect: containerInspectJSON(t, dockerImageInspect{
				ID:          configID,
				OS:          "linux",
				RepoDigests: []string{"ghcr.io/other/agent@sha256:" + strings.Repeat("e", 64), executionTestImage},
			}),
			wantID: configID,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			imageID, err := VerifyContainerImage(testCase.policy, testCase.inspect)
			if err != nil {
				t.Fatalf("valid inspect evidence was refused: %v", err)
			}
			if imageID != testCase.wantID {
				t.Fatalf("imageID = %q, want %q", imageID, testCase.wantID)
			}
		})
	}
}

func TestVerifyContainerImageRefusesUnverifiedEvidence(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 64)
	mutablePolicy := executionTestPolicy()
	mutablePolicy.Image = "ghcr.io/acme/agent:latest"
	invalidPolicy := executionTestPolicy()
	invalidPolicy.CPUCount = 0
	cases := map[string]struct {
		policy  ExecutionPolicy
		inspect []byte
		want    string
	}{
		"invalid policy is refused first": {
			policy:  invalidPolicy,
			inspect: containerInspectJSON(t, dockerImageInspect{ID: "sha256:" + hash, OS: "linux"}),
			want:    "cpu_count",
		},
		"mutable tag policy is refused": {
			policy:  mutablePolicy,
			inspect: containerInspectJSON(t, dockerImageInspect{ID: "sha256:" + hash, OS: "linux"}),
			want:    "mutable tag",
		},
		"malformed JSON": {
			policy:  executionTestPolicy(),
			inspect: []byte(`not-json`),
			want:    "not valid JSON",
		},
		"empty output": {
			policy:  executionTestPolicy(),
			inspect: []byte(``),
			want:    "not valid JSON",
		},
		"empty array": {
			policy:  executionTestPolicy(),
			inspect: []byte(`[]`),
			want:    "no image",
		},
		"multiple entries": {
			policy: executionTestPolicy(),
			inspect: containerInspectJSON(t,
				dockerImageInspect{ID: "sha256:" + hash, OS: "linux", RepoDigests: []string{executionTestImage}},
				dockerImageInspect{ID: "sha256:" + strings.Repeat("b", 64), OS: "linux"},
			),
			want: "exactly one",
		},
		"non-linux operating system": {
			policy:  executionTestPolicy(),
			inspect: containerInspectJSON(t, dockerImageInspect{ID: "sha256:" + hash, OS: "windows", RepoDigests: []string{executionTestImage}}),
			want:    "requires linux",
		},
		"short image ID": {
			policy:  executionTestPolicy(),
			inspect: containerInspectJSON(t, dockerImageInspect{ID: "sha256:abc", OS: "linux", RepoDigests: []string{executionTestImage}}),
			want:    "full sha256",
		},
		"missing repodigest for digest policy": {
			policy:  executionTestPolicy(),
			inspect: containerInspectJSON(t, dockerImageInspect{ID: "sha256:" + hash, OS: "linux"}),
			want:    "RepoDigests",
		},
		"config ID is not a manifest digest": {
			policy:  executionTestPolicy(),
			inspect: containerInspectJSON(t, dockerImageInspect{ID: "sha256:" + hash, OS: "linux"}),
			want:    "RepoDigests",
		},
		"mutable tag in RepoDigests is not an identity": {
			policy:  executionTestPolicy(),
			inspect: containerInspectJSON(t, dockerImageInspect{ID: "sha256:" + hash, OS: "linux", RepoDigests: []string{"ghcr.io/acme/agent:latest"}}),
			want:    "RepoDigests",
		},
		"bare image ID mismatch": {
			policy: func() ExecutionPolicy {
				policy := executionTestPolicy()
				policy.Image = "sha256:" + strings.Repeat("c", 64)
				return policy
			}(),
			inspect: containerInspectJSON(t, dockerImageInspect{ID: "sha256:" + hash, OS: "linux"}),
			want:    "does not match requested image ID",
		},
		"oversize output": {
			policy:  executionTestPolicy(),
			inspect: make([]byte, maxContainerInspectBytes+1),
			want:    "above the",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			imageID, err := VerifyContainerImage(testCase.policy, testCase.inspect)
			if err == nil {
				t.Fatalf("imageID = %q, want a refusal", imageID)
			}
			if imageID != "" {
				t.Fatalf("refused verification returned imageID %q", imageID)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestVerifyContainerCgroupAcceptsTheMandatoryLimits(t *testing.T) {
	t.Parallel()
	want := ContainerVerifiedLimits{MemoryBytes: 8589934592, MemorySwapBytes: 0, CPUCount: 2, PIDsLimit: 128}
	cases := map[string]string{
		"canonical quota and period": "200000 100000\n",
		"equivalent ratio":           "400000 200000",
		"smallest equivalent ratio":  "2 1",
	}
	for name, cpuMax := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			limits, err := VerifyContainerCgroup(executionTestPolicy(), ContainerCgroupValues{
				MemoryMax:     "8589934592\n",
				MemorySwapMax: "0\n",
				CPUMax:        cpuMax,
				PIDsMax:       "128\n",
			})
			if err != nil {
				t.Fatalf("mandatory limits were refused: %v", err)
			}
			if limits != want {
				t.Fatalf("limits = %#v, want %#v", limits, want)
			}
		})
	}
}

func TestVerifyContainerCgroupRefusesNonMandatoryValues(t *testing.T) {
	t.Parallel()
	invalidPolicy := executionTestPolicy()
	invalidPolicy.MemorySwapBytes = 1
	valid := ContainerCgroupValues{
		MemoryMax:     "8589934592",
		MemorySwapMax: "0",
		CPUMax:        "200000 100000",
		PIDsMax:       "128",
	}
	cases := map[string]struct {
		policy ExecutionPolicy
		values ContainerCgroupValues
		want   string
	}{
		"invalid policy is refused first": {
			policy: invalidPolicy,
			values: valid,
			want:   "memory_swap_bytes",
		},
		"memory mismatch": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemoryMax = "4294967296"; return values }(),
			want:   "memory.max",
		},
		"swap above zero": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemorySwapMax = "8589934592"; return values }(),
			want:   "no-swap",
		},
		"pids mismatch": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.PIDsMax = "256"; return values }(),
			want:   "pids.max",
		},
		"cpu ratio mismatch": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = "300000 100000"; return values }(),
			want:   "exactly 2 CPUs",
		},
		"cpu ratio has a remainder": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = "300000 200000"; return values }(),
			want:   "exactly 2 CPUs",
		},
		"cpu unlimited quota": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = "max 100000"; return values }(),
			want:   "unlimited",
		},
		"cpu unlimited period": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = "200000 max"; return values }(),
			want:   "unlimited",
		},
		"cpu missing period": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = "200000"; return values }(),
			want:   "exactly two fields",
		},
		"cpu extra field": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = "200000 100000 1"; return values }(),
			want:   "exactly two fields",
		},
		"cpu zero period": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = "200000 0"; return values }(),
			want:   "must be positive",
		},
		"cpu negative quota": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = "-200000 100000"; return values }(),
			want:   "non-negative decimal integer",
		},
		"missing memory": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemoryMax = ""; return values }(),
			want:   "memory.max is missing",
		},
		"unlimited memory": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemoryMax = "max"; return values }(),
			want:   "memory.max is unlimited",
		},
		"negative memory": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemoryMax = "-8589934592"; return values }(),
			want:   "non-negative decimal integer",
		},
		"malformed memory with units": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemoryMax = "8GiB"; return values }(),
			want:   "non-negative decimal integer",
		},
		"memory overflows int64": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues {
				values := valid
				values.MemoryMax = strings.Repeat("9", 20)
				return values
			}(),
			want: "overflows",
		},
		"memory above the supported bound": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemoryMax = "4611686018427387905"; return values }(),
			want:   "above the supported limit",
		},
		"missing swap": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemorySwapMax = ""; return values }(),
			want:   "memory.swap.max is missing",
		},
		"unlimited swap": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemorySwapMax = "max"; return values }(),
			want:   "memory.swap.max is unlimited",
		},
		"negative swap": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.MemorySwapMax = "-1"; return values }(),
			want:   "non-negative decimal integer",
		},
		"missing pids": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.PIDsMax = ""; return values }(),
			want:   "pids.max is missing",
		},
		"unlimited pids": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.PIDsMax = "max"; return values }(),
			want:   "pids.max is unlimited",
		},
		"malformed pids": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.PIDsMax = "128pids"; return values }(),
			want:   "pids.max",
		},
		"missing cpu": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues { values := valid; values.CPUMax = ""; return values }(),
			want:   "cpu.max",
		},
		"oversize raw value": {
			policy: executionTestPolicy(),
			values: func() ContainerCgroupValues {
				values := valid
				values.MemoryMax = strings.Repeat("8", maxContainerValueBytes+1)
				return values
			}(),
			want: "above the",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			limits, err := VerifyContainerCgroup(testCase.policy, testCase.values)
			if err == nil {
				t.Fatalf("limits = %#v, want a refusal", limits)
			}
			if limits != (ContainerVerifiedLimits{}) {
				t.Fatalf("refused verification returned limits %#v", limits)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

// TestParseContainerCPUFieldsRefusesAnOversizedValue proves the cpu.max parser
// bounds the raw line before splitting it, so a hostile oversized value is
// refused without being interpreted as a quota and period.
func TestParseContainerCPUFieldsRefusesAnOversizedValue(t *testing.T) {
	t.Parallel()
	quota, period, err := parseContainerCPUFields(strings.Repeat("1", maxContainerValueBytes+1))
	if err == nil {
		t.Fatalf("quota %d period %d, want an oversized cpu.max refusal", quota, period)
	}
	if quota != 0 || period != 0 {
		t.Fatalf("refused parse returned quota %d period %d", quota, period)
	}
	if !strings.Contains(err.Error(), "above the") {
		t.Fatalf("error = %v, want the byte bound named", err)
	}
}
