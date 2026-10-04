package shared

import (
	"time"

	"github.com/sneat-dev/wb/internal/age"
)

func HumanAge(d time.Duration) string  { return age.HumanAge(d) }
func PublishedAgo(value string) string { return age.PublishedAgo(value) }
