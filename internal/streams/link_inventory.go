package streams

import (
	"fmt"
	"strings"
)

// readableLinkStreams is the shared inventory admission for both directions of
// worktree dependency links. A corrupt record may hold the requested path;
// its unreadability cannot prove that the path has no links.
func (store *Store) readableLinkStreams() ([]Stream, error) {
	all, unreadable, err := store.List()
	if err != nil {
		return nil, err
	}
	if len(unreadable) > 0 {
		records := make([]string, 0, len(unreadable))
		for _, broken := range unreadable {
			records = append(records, broken.Name+" ("+broken.Path+": "+broken.Reason+")")
		}
		return nil, fmt.Errorf("stream state is unreadable for %s; repair the records before verifying worktree links", strings.Join(records, ", "))
	}
	return all, nil
}
