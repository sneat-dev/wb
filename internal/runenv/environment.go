// Package runenv assembles governed child environments without ambient state.
package runenv

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/runqueue"
	"strings"
)

// Synchronous leaves CPU fields unchanged for an ungoverned command.
func Synchronous(base, argv []string, operationID string, units int, effectiveGOFLAGS string) []string {
	result := merge(base, map[string]string{"WB_OPERATION_ID": operationID})
	if units <= 0 {
		return result
	}
	return merge(result, limits(base, argv, operationID, units, effectiveGOFLAGS))
}

// Worker sets allocation fields even when units is nonpositive.
func Worker(base, argv []string, operationID string, units int, effectiveGOFLAGS string) []string {
	return merge(base, limits(base, argv, operationID, units, effectiveGOFLAGS))
}

// Daemon applies durable additions before allocation overrides. GOMAXPROCS is
// governed from the original inherited base, not the durable additions.
func Daemon(base, argv []string, additions map[string]string, operationID string, units int, effectiveGOFLAGS string) []string {
	return merge(merge(base, additions), limits(base, argv, operationID, units, effectiveGOFLAGS))
}

func limits(base, argv []string, operationID string, units int, flags string) map[string]string {
	result := map[string]string{
		"GOMAXPROCS":  runqueue.GovernGOMAXPROCS(runqueue.LookupEnv(base, "GOMAXPROCS"), units),
		"NX_PARALLEL": fmt.Sprint(units), "WB_CPU_UNITS": fmt.Sprint(units), "WB_OPERATION_ID": operationID,
	}
	// Non-Go commands retain nonempty resolved flags too: this is the existing
	// GovernGoFlags contract, not a reason to filter by program name here.
	if governed := runqueue.GovernGoFlags(argv, flags, units); governed != "" {
		result["GOFLAGS"] = governed
	}
	return result
}

func merge(base []string, additions map[string]string) []string {
	result := append([]string(nil), base...)
	for key, value := range additions {
		prefix := key + "="
		filtered := result[:0]
		for _, entry := range result {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, prefix+value)
	}
	return result
}
