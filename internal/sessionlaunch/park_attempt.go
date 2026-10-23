package sessionlaunch

import (
	"context"
	"fmt"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

// InspectPreparedParkAttempt returns the native prepared attempt only when its
// evidence authenticates the explicitly supplied parked aggregate authority.
func InspectPreparedParkAttempt(ctx context.Context, options Options) (string, error) {
	evidence, err := InspectPrepared(ctx, options)
	if err != nil {
		return "", err
	}
	if options.Authority == nil || !evidence.Authenticates(options.Authority.AggregateID, sessionmove.Digest(options.Authority.AggregateDigest)) {
		return "", fmt.Errorf("prepared local launcher evidence is not authenticated to the exact parked aggregate")
	}
	return evidence.AttemptID, nil
}
