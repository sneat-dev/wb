// The repositories of a model, merged by identity across machines: not needed by
// the shell or the first paint of Home, so a function over the model in a lazy
// entry point (`@cockpit/fleet-data/list`) rather than a getter of it.

import type { FleetModel } from './fleet-model'
import { MergedRepository } from './repository-identity'
import { mergeRepositories } from './repository-merge'

/** One merged repository per lower-cased `owner/name` across machines, computed once per model. */
export function buildRepositories(model: FleetModel): MergedRepository[] {
  return model.memo('repositories', [], () => mergeRepositories(model.document.repositories, model.now, { pullRequests: model.document.pull_requests, agents: model.document.agents }))
}
