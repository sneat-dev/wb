package fleet_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cockpit/fleet"
)

// stringPaths lists, as desk.WithheldAtRepositories spells them, every JSON
// path in value's type that can carry text: a string, or a slice of strings.
func stringPaths(value any) []string {
	seen := map[string]bool{}
	var walk func(reflect.Type, string, map[reflect.Type]bool)
	walk = func(kind reflect.Type, prefix string, visiting map[reflect.Type]bool) {
		for kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		switch {
		case kind == reflect.TypeFor[time.Time]():
			return
		case kind.Kind() == reflect.String:
			seen[prefix] = true
			return
		case kind.Kind() == reflect.Slice || kind.Kind() == reflect.Array:
			if kind.Elem().Kind() == reflect.String {
				seen[prefix] = true
				return
			}
			walk(kind.Elem(), prefix+"[]", visiting)
			return
		case kind.Kind() == reflect.Map:
			seen[prefix+"{}"] = true
			return
		case kind.Kind() != reflect.Struct || visiting[kind]:
			return
		}
		visiting[kind] = true
		defer delete(visiting, kind)
		for index := range kind.NumField() {
			field := kind.Field(index)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if field.Anonymous && name == "" {
				walk(field.Type, prefix, visiting)
				continue
			}
			if name == "" {
				name = field.Name
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			walk(field.Type, path, visiting)
		}
	}
	walk(reflect.TypeOf(value), "", map[reflect.Type]bool{})
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// shownAtRepositories is every text path of the export envelope that the
// `repositories` level shows. Identities, states and closed-set codes are
// shown; names a person chose are withheld (desk.WithheldAtRepositories).
// Entry ids are shown because the reduction re-keys the name-derived ones.
var shownAtRepositories = []string{
	"machine",
	"metrics.reason",
	"metrics.route",
	"fleet.code_index_provider",
	"fleet.error",
	"fleet.agents[].activity",
	"fleet.agents[].id",
	"fleet.agents[].kind",
	"fleet.agents[].machine",
	"fleet.agents[].machine_id",
	"fleet.agents[].model",
	"fleet.agents[].repository",
	"fleet.agents[].route",
	"fleet.agents[].run_id",
	"fleet.agents[].runtime",
	"fleet.agents[].session_id",
	"fleet.agents[].state",
	"fleet.machines[].arch",
	"fleet.machines[].desk_error",
	"fleet.machines[].id",
	"fleet.machines[].machine",
	"fleet.machines[].machine_id",
	"fleet.machines[].os",
	"fleet.machines[].publish_error",
	"fleet.machines[].remote_error",
	"fleet.machines[].route",
	"fleet.machines[].transport",
	"fleet.machines[].wb_version",
	"fleet.pull_requests[].id",
	"fleet.pull_requests[].machine",
	"fleet.pull_requests[].machine_id",
	"fleet.pull_requests[].mergeable",
	"fleet.pull_requests[].repository",
	"fleet.pull_requests[].route",
	"fleet.pull_requests[].state",
	"fleet.pull_requests[].url",
	"fleet.repositories[].code_index[].indexer",
	"fleet.repositories[].code_index[].state",
	"fleet.repositories[].code_index[].statistics.error",
	"fleet.repositories[].code_index[].statistics.kinds[].kind",
	"fleet.repositories[].default_branch",
	"fleet.repositories[].error",
	"fleet.repositories[].host",
	"fleet.repositories[].id",
	"fleet.repositories[].machine",
	"fleet.repositories[].machine_id",
	"fleet.repositories[].name",
	"fleet.repositories[].remote_url_web",
	"fleet.repositories[].route",
	"fleet.throughput.per_day[].date",
	"fleet.worktrees[].code_index[].indexer",
	"fleet.worktrees[].code_index[].state",
	"fleet.worktrees[].code_index[].statistics.error",
	"fleet.worktrees[].code_index[].statistics.kinds[].kind",
	"fleet.worktrees[].id",
	"fleet.worktrees[].lifecycle",
	"fleet.worktrees[].machine",
	"fleet.worktrees[].machine_id",
	"fleet.worktrees[].owner_state",
	"fleet.worktrees[].repository",
	"fleet.worktrees[].route",
}

// TestEveryEnvelopeTextFieldIsClassifiedForTheRepositoriesLevel keeps the
// Desk level check and the export in step: a text field added to the export
// fails here until it is listed as shown, or as withheld in api/desk, which
// Desk then refuses at `repositories` (desk-connect#req:detail-level-applied-on-the-machine).
func TestEveryEnvelopeTextFieldIsClassifiedForTheRepositoriesLevel(t *testing.T) {
	t.Parallel()
	classified := map[string]string{}
	for _, path := range shownAtRepositories {
		classified[path] = "shown"
	}
	for _, path := range desk.WithheldAtRepositories() {
		if classified[path] != "" {
			t.Errorf("%s is both shown and withheld", path)
		}
		classified[path] = "withheld"
	}
	present := map[string]bool{}
	for _, path := range stringPaths(fleet.Envelope{}) {
		present[path] = true
		if classified[path] == "" {
			t.Errorf("the export's text field %s is neither shown nor withheld at repositories detail", path)
		}
	}
	for path, class := range classified {
		if !present[path] {
			t.Errorf("%s field %s is not in the export", class, path)
		}
	}
}
