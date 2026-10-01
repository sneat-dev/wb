// The fleet-data library, first-page entry point: the schema 2 types and strict
// client, the store, the view model with Home's section derivations, the task
// state, the link builders and what the shell needs to render. Pure TypeScript
// apart from the store and client, which are Angular services. See README.md.
//
// What the first page does not render is behind separate entry points that
// pages import lazily, so the shell and Home do not carry it:
//   @cockpit/fleet-data/panel     the per-entity panels (`buildWorktreePanel`, ...)
//   @cockpit/fleet-data/commands  every Copy-command template
//   @cockpit/fleet-data/list      the list rows, `applyListQuery` and the full filter vocabulary
export * from './lib/fleet.types'
export * from './lib/fleet-client'
export * from './lib/fleet-store'
export * from './lib/fleet-view'
export * from './lib/matcher'
export * from './lib/vocabulary'
export * from './lib/task-state'
export * from './lib/repository-identity'
export * from './lib/view-types'
export * from './lib/fleet-model'
export * from './lib/web-address'
export * from './lib/control.types'
export {
  PLACEHOLDERS,
  commandTarget,
  cockpitExport,
  daemonStart,
  fleetStatus,
  pullRequestLand,
  remoteEnroll,
  remotePublish,
  selfUpdate,
  shellQuote,
  sshRouteOf,
  valueProblem,
} from './lib/command-core'
export type { CommandTarget, CopyCommand, SshRoute } from './lib/command-core'
export type { PanelBase, PanelCommand, WorktreePanel, TaskPanel, RepositoryPanel, AgentPanel, MachinePanel, PullRequestPanel } from './lib/entity-views'
export type { ListRow, ListResult } from './lib/list-rows'
export type { CodeIndexView, RepositoryOption } from './lib/page-helpers'
