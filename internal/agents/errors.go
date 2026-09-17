package agents

import (
	"errors"
	"fmt"
)

// RequestError marks a refusal of the invocation itself: the caller asked for
// something WB cannot even attempt, such as an unconfigured profile. It is
// deliberately distinguishable from a run that started and then failed,
// because the two need different exit codes and different caller responses.
type RequestError struct {
	message string
}

func (err *RequestError) Error() string { return err.message }

func requestErrorf(format string, args ...any) error {
	return &RequestError{message: fmt.Sprintf(format, args...)}
}

// IsRequestError reports whether an error refused the invocation.
func IsRequestError(err error) bool {
	var refused *RequestError
	return errors.As(err, &refused)
}
