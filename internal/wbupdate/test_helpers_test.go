package wbupdate

import (
	"time"
)

func testService() Service {
	return Service{RunChild: RunChild, HandoffTimeout: func() time.Duration { return time.Second }}
}
