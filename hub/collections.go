package hub

// Collections lists every collection path shape the hub-owned stores write
// to, in declaration order: a root collection always precedes the
// subcollections nested beneath it.
//
// A schemaless engine (Firestore, OpenVaultDB, dalgo2memory) never needs this
// — a collection springs into existence on first write. A schema-first engine
// does: inGitDB refuses a write to a collection that has no definition on
// disk, and a nested path must be declared as a subcollection of its root.
// The dynamic segments in a real path (a machine id, an identity digest, a
// generation) are document ids, not collection names, so the shapes here are
// the complete and finite set.
//
// Keep this in step with the collection constants and path helpers in
// installation_store.go, repository_event_store.go,
// machine_credential_store.go, machine_snapshot_store.go and
// poll_observation_store.go; the store tests assert every one of them is
// listed.
func Collections() []string {
	return []string{
		machineCredentialCollection,
		machineEnrollmentCollection,
		machineSnapshotCollection,
		pollObservationCollection,

		installationStateCollection,
		installationIdentityCollection,
		installationIdentityCollection + "/generations",
		installationIdentityCollection + "/generations/installations",
		installationIdentityCollection + "/generations/installations/repositories",
		installationIdentityCollection + "/generations/installations/repository_generations",
		installationIdentityCollection + "/generations/installations/repository_generations/chunks",
		installationIndexCollection,
		installationUserIndexCollection,
		installationLifecycleCollection,

		repositoryEventCollection,
		repositoryEventMetaCollection,
		repositoryEventQueueCollection,
		repositoryEventQueueCollection + "/" + repositoryEventQueueEvents,
		repositoryEventQueueCollection + "/" + repositoryEventQueuePolls,
		repositoryEventStatusCollection,
		repositoryEventStatusCollection + "/" + repositoryEventStatusPending,
	}
}
