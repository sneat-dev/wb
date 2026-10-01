import { Agent, AppLink, FleetModel, MachineView, MergedRepository, TaskView, Worktree, ListPageId, Term, agentDetailLink, declaredFields, machineDetailLink, parseQuery, repositoryDetailLink, taskDetailLink, worktreeDetailLink } from '@cockpit/fleet-data'
import { EXACT_FIELDS, ListRow, MatchEnv, Subject, agentLabel, buildAgentRows, buildMachineRows, buildRepositoryRows, buildTaskRows, buildWorktreeRows, matchesTerms } from '@cockpit/fleet-data/list'
import { IconName } from '../ui/icon'

export type PaletteKind = 'task' | 'repository' | 'worktree' | 'branch' | 'agent' | 'machine'

/** At most this many results per kind (REQ:command-palette). */
export const RESULTS_PER_KIND = 8

export interface PaletteResult {
  /** Stable across polls: kind and the entity id. */
  id: string
  kind: PaletteKind
  label: string
  detail: string
  link: AppLink
}

export interface PaletteGroup {
  kind: PaletteKind
  title: string
  icon: IconName
  results: PaletteResult[]
  /** Matches beyond the cap, for the "n more" line. */
  more: number
}

/** The kinds in the order the palette shows them: the task first, then the inventory. */
export const PALETTE_KINDS: readonly { kind: PaletteKind; title: string; icon: IconName }[] = [
  { kind: 'task', title: 'Tasks', icon: 'list-checks' },
  { kind: 'repository', title: 'Repositories', icon: 'folder' },
  { kind: 'worktree', title: 'Worktrees', icon: 'layers' },
  { kind: 'branch', title: 'Branches', icon: 'git-branch' },
  { kind: 'agent', title: 'Agents', icon: 'bot' },
  { kind: 'machine', title: 'Machines', icon: 'server' },
]

/** Where a value matches: whole, at its start, at a word start (after `/ - _ . :` or a space), or inside. */
function placement(value: string, text: string): number {
  if (value === text) return 0
  if (value.startsWith(text)) return 1
  return /[\s/_.:-]/.test(value.charAt(value.indexOf(text) - 1)) ? 2 : 3
}

/** A lower number ranks first: how well the plain terms match the subject's bare values. */
function rank(terms: readonly Term[], subject: Subject): number {
  let total = 0
  for (const term of terms) {
    if (term.negate || term.field !== undefined) continue
    // A glob, which has no single place, ranks after the placed matches.
    // A repository is `owner/name`: its name alone counts as a value too, so `go` is the whole name of `acme/go`.
    const values = subject.bare.flatMap((value) => [value, value.slice(value.lastIndexOf('/') + 1)])
    total += values.reduce((least, value) => (value.includes(term.raw) ? Math.min(least, placement(value, term.raw)) : least), 3)
  }
  return total
}

type Described = Pick<PaletteResult, 'label' | 'detail' | 'link'>

interface Candidate {
  id: string
  subject: Subject
  /** Built only for the results that are shown: matching a thousand rows builds no label. */
  describe: () => Described
}

function group(kind: PaletteKind, candidates: readonly Candidate[], page: ListPageId | 'branch', terms: readonly Term[], now: number): PaletteGroup | undefined {
  const env: MatchEnv = {
    declared: page === 'branch' ? new Set(['branch', 'repo', 'task']) : declaredFields(page),
    exact: EXACT_FIELDS,
    now,
  }
  const matching = candidates
    .filter((candidate) => matchesTerms(terms, candidate.subject, env))
    .map((candidate, index) => ({ candidate, index, score: rank(terms, candidate.subject) }))
    .sort((a, b) => a.score - b.score || (b.candidate.subject.activityAt ?? 0) - (a.candidate.subject.activityAt ?? 0) || a.index - b.index)
  if (matching.length === 0) return undefined
  const info = PALETTE_KINDS.find((candidate) => candidate.kind === kind) as (typeof PALETTE_KINDS)[number]
  return {
    kind,
    title: info.title,
    icon: info.icon,
    results: matching.slice(0, RESULTS_PER_KIND).map(({ candidate }) => ({ id: `${kind}:${candidate.id}`, kind, ...candidate.describe() })),
    more: Math.max(0, matching.length - RESULTS_PER_KIND),
  }
}

/** How each kind of entity reads in the palette: its label, its detail and where it opens. */
function describers(model: FleetModel) {
  return {
    task: (row: ListRow<TaskView>): Described => ({
      label: row.item.name,
      detail: [row.item.stateInfo.label, ...row.item.repositories.slice(0, 2)].join(' · '),
      link: taskDetailLink(row.item.name),
    }),
    repository: (row: ListRow<MergedRepository>): Described => ({
      label: row.item.slug,
      detail: [...new Set(row.item.checkouts.map((checkout) => checkout.machine))].join(', '),
      link: repositoryDetailLink(row.item.host, row.item.slug),
    }),
    worktree: (row: ListRow<Worktree>): Described => ({
      label: row.item.task,
      detail: `${row.item.branch} · ${model.repositoryName(row.item.repository)}`,
      link: worktreeDetailLink(row.item.id),
    }),
    branch: (worktree: Worktree): Described => ({
      label: worktree.branch,
      detail: `${model.repositoryName(worktree.repository)} · ${worktree.task}`,
      link: worktreeDetailLink(worktree.id),
    }),
    agent: (row: ListRow<Agent>): Described => ({
      label: agentLabel(row.item),
      detail: [model.tasksOfAgent(row.item)[0], row.item.repository === undefined ? undefined : model.repositoryName(row.item.repository), row.item.machine].filter((part) => part).join(' · '),
      link: agentDetailLink(row.item.id),
    }),
    machine: (row: ListRow<MachineView>): Described => ({
      label: row.item.machine.machine,
      detail: row.item.local ? `${row.item.state}, this machine` : row.item.state,
      link: machineDetailLink(row.item.machine.id),
    }),
  }
}

const rows = <T>(source: readonly ListRow<T>[], describe: (row: ListRow<T>) => Described, alsoBare?: (row: ListRow<T>) => string[]): Candidate[] =>
  source.map((row) => ({
    id: row.id,
    subject: alsoBare ? { ...row.subject, bare: [...row.subject.bare, ...alsoBare(row)] } : row.subject,
    describe: () => describe(row),
  }))

const branchKey = (worktree: Worktree) => `${worktree.repository}|${worktree.branch}`

/** An agent is also found by its session or run id, which is what the operator holds. */
const agentIds = (row: ListRow<Agent>): string[] => [row.item.session_id, row.item.run_id].filter((id): id is string => id !== undefined).map((id) => id.toLowerCase())

/**
 * The palette's results for `text`, grouped by kind with at most 8 each: the
 * entities of the fleet document (the branches are those of the worktrees it
 * lists; the lazily loaded branches of a repository page are not searched),
 * matched with the same matcher as the list filters. No text gives no groups.
 * Matching comes first; a label and detail are built only for what is shown.
 */
export function searchPalette(model: FleetModel, text: string, now: number): PaletteGroup[] {
  const terms = parseQuery(text)
  if (terms.length === 0) return []
  const describe = describers(model)
  const worktreeRows = buildWorktreeRows(model)
  const branches: Candidate[] = []
  const seen = new Set<string>()
  for (const row of worktreeRows) {
    const worktree = row.item
    const key = branchKey(worktree)
    if (worktree.branch === '' || seen.has(key)) continue
    seen.add(key)
    const repository = model.repositoryName(worktree.repository).toLowerCase()
    branches.push({
      id: key,
      describe: () => describe.branch(worktree),
      subject: {
        bare: [worktree.branch.toLowerCase(), repository],
        fields: { branch: [worktree.branch.toLowerCase()], repo: [repository], task: [worktree.task.toLowerCase()] },
        ...(row.subject.activityAt === undefined ? {} : { activityAt: row.subject.activityAt }),
      },
    })
  }
  const groups = [
    group('task', rows(buildTaskRows(model), describe.task), 'tasks', terms, now),
    group('repository', rows(buildRepositoryRows(model), describe.repository), 'repositories', terms, now),
    group('worktree', rows(worktreeRows, describe.worktree), 'worktrees', terms, now),
    group('branch', branches, 'branch', terms, now),
    group('agent', rows(buildAgentRows(model), describe.agent, agentIds), 'agents', terms, now),
    group('machine', rows(buildMachineRows(model), describe.machine), 'machines', terms, now),
  ]
  return groups.filter((candidate): candidate is PaletteGroup => candidate !== undefined)
}

/**
 * The result a remembered id (`kind:entity`) is now, read from the model, so
 * its label and detail are never what they were when it was remembered; undefined
 * when the entity is gone.
 */
export function resolveResult(model: FleetModel, id: string): PaletteResult | undefined {
  const separator = id.indexOf(':')
  const kind = id.slice(0, separator)
  const entity = id.slice(separator + 1)
  const describe = describers(model)
  const found = (described: Described | undefined): PaletteResult | undefined => (described ? { id, kind: kind as PaletteKind, ...described } : undefined)
  switch (kind) {
    case 'task': {
      const row = buildTaskRows(model).find((candidate) => candidate.id === entity)
      return found(row && describe.task(row))
    }
    case 'repository': {
      const row = buildRepositoryRows(model).find((candidate) => candidate.id === entity)
      return found(row && describe.repository(row))
    }
    case 'worktree': {
      const row = buildWorktreeRows(model).find((candidate) => candidate.id === entity)
      return found(row && describe.worktree(row))
    }
    case 'branch': {
      const worktree = buildWorktreeRows(model).find((candidate) => branchKey(candidate.item) === entity)
      return found(worktree && describe.branch(worktree.item))
    }
    case 'agent': {
      const row = buildAgentRows(model).find((candidate) => candidate.id === entity)
      return found(row && describe.agent(row))
    }
    case 'machine': {
      const row = buildMachineRows(model).find((candidate) => candidate.id === entity)
      return found(row && describe.machine(row))
    }
    default:
      return undefined
  }
}
