package repositoryevents

import "github.com/sneat-dev/wb/api/githubapp/repositoryevent"

// Helpers kept for tests only: no production caller remains.

func (store CursorStore) Load() (string, error) {
	state, err := store.loadState()
	return state.Cursor, err
}

func (store CursorStore) Save(cursor string) error {
	return store.saveState(cursorState{Version: repositoryevent.ContractVersion, Cursor: cursor})
}
