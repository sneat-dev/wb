// List rows: each page's entities prepared once per document, with their
// lower-cased searchable values, chips and sort keys, so that typing in a
// filter only walks precomputed strings. `applyListQuery` filters and sorts.

import { type FleetModel, parseVersion } from './fleet-model'
import { Agent, Machine, Worktree } from './fleet.types'
import { MatchEnv, StepCounter, Subject, idleOver30Days, matchesTerms } from './match'
import { Term, parseQuery } from './matcher'
import { MergedRepository } from './repository-identity'
import { TaskView, MachineView } from './view-types'
import { VOCABULARY, defaultDirection } from './filter-vocabulary'
import { buildCleanup } from './home-details'
import { buildRepositories } from './model-repositories'
import { ListPageId, ListQuery, declaredFields } from './vocabulary'
import { isRunning } from './fleet.types'

/** A version as a string that sorts like the version: each number padded, so 0.9.0 sorts before 0.10.0. */
export function versionKey(version: string | undefined): string | undefined {
  return parseVersion(version)?.map((part) => String(part).padStart(8, '0')).join('.')
}

export type SortKey = string | number | undefined

/** `state` is matched whole: `state:idle` is not `state:idle-ish`. */
export const EXACT_FIELDS: ReadonlySet<string> = new Set(['state'])

export interface ListRow<T> {
  /** The row's `sel` value on its page. */
  id: string
  item: T
  subject: Subject
  /** The machine ids the row belongs to, for the machine chips. */
  machineIds: readonly string[]
  /** The chips that hold for the row, except `idle30`, which depends on the clock. */
  chips: ReadonlySet<string>
  /**
   * A task at risk whose last activity is outside the Needs you window: the list leaves it out unless the `older`
   * chip is on or the filter asks for `state:at-risk`, as Home leaves it to Cleanup (`includesOlder`).
   */
  windowed?: boolean
  sortKeys: Readonly<Record<string, SortKey>>
}

const lower = (values: readonly (string | undefined)[]): string[] => values.filter((value): value is string => value !== undefined).map((value) => value.toLowerCase())

function activity(time: number | undefined): { activityAt?: number } {
  return time === undefined ? {} : { activityAt: time }
}

export function taskRows(tasks: readonly TaskView[], needsYouTasks: ReadonlySet<string>, olderAtRisk: ReadonlySet<string> = new Set()): ListRow<TaskView>[] {
  return tasks.map((task) => {
    const chips = new Set<string>()
    if (needsYouTasks.has(task.name)) chips.add('needs-you')
    if (task.state === 'ready') chips.add('ready')
    if (task.state === 'working') chips.add('working')
    if (task.agents.length > 0) chips.add('agent')
    if (task.pullRequests.length > 0) chips.add('pr')
    if (task.repositories.length > 1) chips.add('multirepo')
    const repositories = lower(task.repositories)
    return {
      id: task.name,
      item: task,
      subject: {
        bare: [task.name.toLowerCase(), ...repositories],
        fields: { task: [task.name.toLowerCase()], repo: repositories, machine: lower(task.machines.map((machine) => machine.name)), state: [task.state] },
        ...activity(task.lastActivityAt),
      },
      machineIds: task.machines.map((machine) => machine.id),
      chips,
      ...(olderAtRisk.has(task.name) ? { windowed: true } : {}),
      sortKeys: {
        task: task.name.toLowerCase(),
        state: task.stateInfo.rank,
        worktrees: task.worktrees.length,
        activity: task.lastActivityAt,
      },
    }
  })
}

export function repositoryRows(repositories: readonly MergedRepository[]): ListRow<MergedRepository>[] {
  return repositories.map((repository) => {
    const chips = new Set<string>()
    if (repository.worktreeCount > 0) chips.add('worktrees')
    if ((repository.activeAgentCount ?? 0) > 0) chips.add('agents')
    if ((repository.openPullRequestCount ?? 0) > 0) chips.add('prs')
    if (repository.codeIndex === 'stale' || repository.codeIndex === 'diverged' || repository.codeIndex === 'failed') chips.add('index')
    if (repository.errors.length > 0) chips.add('errors')
    const slug = repository.slug.toLowerCase()
    const branches = repository.localBranchCount === undefined && repository.remoteBranchCount === undefined ? undefined : (repository.localBranchCount ?? 0) + (repository.remoteBranchCount ?? 0)
    return {
      id: repository.id,
      item: repository,
      subject: {
        bare: [slug],
        fields: { repo: [slug], machine: lower(repository.checkouts.map((checkout) => checkout.machine)), state: repository.codeIndexStates },
        ...activity(repository.lastActivityAt),
      },
      machineIds: repository.checkouts.map((checkout) => checkout.machineId),
      chips,
      sortKeys: { repository: slug, activity: repository.lastActivityAt, worktrees: repository.worktreeCount, branches },
    }
  })
}

/** What the worktree rows need besides the worktrees. */
export interface WorktreeRowContext {
  /** `owner/name` of a repository id; the id itself when the document does not list it. */
  repositoryName: (id: string) => string
  /** The worktrees that have a pull request. */
  withPullRequest: ReadonlySet<string>
  /** The cleanup sets of REQ:home-cleanup. */
  safeIds: ReadonlySet<string>
  lookIds: ReadonlySet<string>
}

export function worktreeRows(worktrees: readonly Worktree[], context: WorktreeRowContext): ListRow<Worktree>[] {
  return worktrees.map((worktree) => {
    const chips = new Set<string>()
    if (worktree.owner_state === 'active') chips.add('active')
    if (worktree.owner_state === 'orphaned') chips.add('orphaned')
    if ((worktree.ahead ?? 0) > 0) chips.add('unpushed')
    if (worktree.upstream_gone === true) chips.add('gone')
    if (context.withPullRequest.has(worktree.id)) chips.add('pr')
    if (context.safeIds.has(worktree.id)) chips.add('safe')
    if (context.lookIds.has(worktree.id)) chips.add('look')
    const time = worktree.last_activity_at ? Date.parse(worktree.last_activity_at) : Number.NaN
    const at = Number.isNaN(time) ? undefined : time
    const task = worktree.task.toLowerCase()
    const repository = context.repositoryName(worktree.repository).toLowerCase()
    const branch = worktree.branch.toLowerCase()
    return {
      id: worktree.id,
      item: worktree,
      subject: {
        bare: [task, repository, branch],
        fields: { task: [task], repo: [repository], branch: [branch], machine: [worktree.machine.toLowerCase()], state: worktree.owner_state ? [worktree.owner_state] : [] },
        ...activity(at),
      },
      machineIds: [worktree.machine_id],
      chips,
      sortKeys: { worktree: task, state: worktree.owner_state, machine: worktree.machine.toLowerCase(), activity: at },
    }
  })
}

/** What the agent rows need besides the agents. */
export interface AgentRowContext {
  taskOf: (agent: Agent) => string | undefined
  repositoryName: (id: string) => string
}

export function agentRows(agents: readonly Agent[], context: AgentRowContext): ListRow<Agent>[] {
  return agents.map((agent) => {
    const chips = new Set<string>()
    const running = isRunning(agent)
    if (running) chips.add('running')
    if (agent.activity === 'blocked') chips.add('blocked')
    const runtime = agent.runtime?.toLowerCase()
    if (runtime !== undefined) chips.add(`runtime-${runtime}`)
    const task = context.taskOf(agent)?.toLowerCase()
    const repository = agent.repository === undefined ? undefined : context.repositoryName(agent.repository).toLowerCase()
    const started = agent.started_at ? Date.parse(agent.started_at) : Number.NaN
    const startedAt = Number.isNaN(started) ? undefined : started
    return {
      id: agent.id,
      item: agent,
      subject: {
        bare: lower([runtime, agent.model, task, repository]),
        fields: {
          runtime: lower([runtime]),
          task: lower([task]),
          repo: lower([repository]),
          machine: [agent.machine.toLowerCase()],
          state: lower([agent.activity, agent.state]),
        },
      },
      machineIds: [agent.machine_id],
      chips,
      sortKeys: {
        label: `${runtime ?? ''} ${(agent.model ?? '').toLowerCase()} ${task ?? repository ?? ''}`,
        activity: agent.activity,
        machine: agent.machine.toLowerCase(),
        started: startedAt,
        // The default order, which has no column: running agents first, then the newest start.
        running: (running ? 1e14 : 0) + (startedAt ?? 0),
      },
    }
  })
}

export function machineRows(machines: readonly MachineView[], version: (machine: Machine) => string | undefined): ListRow<MachineView>[] {
  return machines.map((view) => {
    const chips = new Set<string>()
    if (view.state === 'stale') chips.add('stale')
    if (view.outdated) chips.add('outdated')
    const name = view.machine.machine.toLowerCase()
    return {
      id: view.machine.id,
      item: view,
      subject: { bare: [name], fields: { machine: [name], state: [view.state] } },
      machineIds: [view.machine.id],
      chips,
      sortKeys: {
        // The default order, which has no column: this machine first, then by name.
        local: `${view.local ? 0 : 1}${name}`,
        machine: name,
        state: view.state,
        version: version(view.machine),
      },
    }
  })
}

function compare(a: SortKey, b: SortKey): number {
  if (a === b) return 0
  if (a === undefined) return 1
  if (b === undefined) return -1
  return a < b ? -1 : 1
}

/** Newest first, and a row with no activity time last. */
function newestFirst(a: SortKey, b: SortKey): number {
  if (a === b) return 0
  if (a === undefined) return 1
  if (b === undefined) return -1
  return a < b ? 1 : -1
}

export interface ListResult<T> {
  rows: ListRow<T>[]
  /** The number of rows before filtering: the "of 438". */
  total: number
}

/**
 * Filters `rows` by the machine selection, chips and filter text of `query`,
 * then sorts them: by the query's sort, or the page's default. A row with no
 * value for the sort comes last in both directions; ties keep the row order.
 */
/** Whether a query lists the rows its page leaves out of the window: the `older` chip, or a `state:at-risk` term of its own. */
export function includesOlder(query: ListQuery, terms: readonly Term[]): boolean {
  return query.chips.includes('older') || terms.some((term) => !term.negate && term.field === 'state' && term.value === 'at-risk')
}

export function applyListQuery<T>(page: ListPageId, rows: readonly ListRow<T>[], query: ListQuery, now: number, counter?: StepCounter): ListResult<T> {
  const terms = parseQuery(query.q)
  const env: MatchEnv = { declared: declaredFields(page), exact: EXACT_FIELDS, now, counter }
  const machines = new Set(query.machines)
  const chips = query.chips.filter((chip) => chip !== 'older')
  const older = includesOlder(query, terms)
  const matching = rows.filter((row) => {
    if (row.windowed === true && !older) return false
    if (machines.size > 0 && !row.machineIds.some((id) => machines.has(id))) return false
    for (const chip of chips) {
      if (chip === 'idle30' ? !idleOver30Days(row.subject.activityAt, now) : !row.chips.has(chip)) return false
    }
    return terms.length === 0 || matchesTerms(terms, row.subject, env)
  })
  // A sort column the page does not list is ignored.
  const requested = query.sort !== undefined && VOCABULARY[page].sorts.includes(query.sort) ? query.sort : undefined
  const sort = requested ?? VOCABULARY[page].defaultSort.sort
  const dir = query.dir ?? (requested === undefined ? VOCABULARY[page].defaultSort.dir : defaultDirection(page, requested))
  const sign = dir === 'asc' ? 1 : -1
  const sorted = matching
    .map((row, index) => ({ row, index }))
    .sort((a, b) => {
      const x = a.row.sortKeys[sort]
      const y = b.row.sortKeys[sort]
      // A missing value is last whichever way the column runs.
      const order = x === undefined || y === undefined ? compare(x, y) : sign * compare(x, y)
      // Tasks of one state are newest first (the default order of the Tasks list).
      const tie = page === 'tasks' && sort === 'state' ? newestFirst(a.row.sortKeys['activity'], b.row.sortKeys['activity']) : 0
      return order || tie || a.index - b.index
    })
    .map((entry) => entry.row)
  return { rows: sorted, total: rows.length }
}

// ---- the row sets of a model, memoised per document (REQ:derived-collections-memoised) ----

/** The Tasks rows of the model; the `needs-you` chip holds for exactly the tasks Home lists. */
export function buildTaskRows(model: FleetModel): ListRow<TaskView>[] {
  return model.memo('rows:tasks', [], () =>
    taskRows(
      model.tasks,
      new Set(model.needsYou.items.map((item) => item.task)),
      new Set(model.tasks.filter((task) => task.state === 'at-risk' && !model.isRecent(task)).map((task) => task.name)),
    ),
  )
}

export function buildRepositoryRows(model: FleetModel): ListRow<MergedRepository>[] {
  return model.memo('rows:repositories', [], () => repositoryRows(buildRepositories(model)))
}

/** The Worktrees rows; the `pr` chip reads `model.worktreePullRequests`, the join the worktree panel reads too. */
export function buildWorktreeRows(model: FleetModel): ListRow<Worktree>[] {
  return model.memo('rows:worktrees', [], () =>
    worktreeRows(model.document.worktrees, {
      repositoryName: (id) => model.repositoryName(id),
      withPullRequest: new Set(model.worktreePullRequests.keys()),
      safeIds: buildCleanup(model).safeIds,
      lookIds: buildCleanup(model).lookIds,
    }),
  )
}

export function buildAgentRows(model: FleetModel): ListRow<Agent>[] {
  return model.memo('rows:agents', [], () => agentRows(model.document.agents, { taskOf: (agent) => model.tasksOfAgent(agent)[0], repositoryName: (id) => model.repositoryName(id) }))
}

export function buildMachineRows(model: FleetModel): ListRow<MachineView>[] {
  return model.memo('rows:machines', [], () => machineRows(model.machines, (machine) => versionKey(machine.wb_version)))
}
