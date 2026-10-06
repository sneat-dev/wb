package landingcontext

import (
	"github.com/sneat-dev/wb/internal/streams"
)

const FleetEventLogName = ".fleet"

func Events(projectsRoot, repository string) (streams.EventAppender, string) {
	return eventsWith(projectsRoot, repository, streams.Open)
}
func eventsWith(projectsRoot, repository string, open func(string) (*streams.Store, error)) (streams.EventAppender, string) {
	store, err := open(projectsRoot)
	if err != nil {
		return streams.DiscardEvents{}, ""
	}
	if stream, found, _, streamErr := store.RepositoryStream(repository); streamErr == nil && found {
		return store.EventLog(stream.Name), stream.Name
	}
	// Outside every stream the event still belongs somewhere: the analytics
	// exist to measure verbs, not only streams, and a landing that leaves no
	// record is a landing the report cannot count.
	return store.EventLog(FleetEventLogName), ""
}
