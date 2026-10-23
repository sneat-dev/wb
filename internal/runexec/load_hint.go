package runexec

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/hostload"
)

func LoadHint(configPath string) string {
	return loadHint(configPath, hostload.Resolve, hostload.System)
}
func loadHint(configPath string, resolve func(string) (float64, string), system func() (float64, error)) string {
	floor, reason := resolve(configPath)
	if reason != "" || floor <= 0 {
		return "capacity contention"
	}
	load, err := system()
	if err != nil {
		return "capacity contention"
	}
	return fmt.Sprintf("host load %.1f>%.1f", load, floor)
}
