package shared

import (
	"fmt"
	"time"
)

func HumanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
func PublishedAgo(age string) string {
	if age == "just now" {
		return age
	}
	return age + " ago"
}
