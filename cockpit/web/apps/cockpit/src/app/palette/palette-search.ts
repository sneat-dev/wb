import {
  AppLink,
  FleetModel,
  ListPageId,
  ListRow,
  MatchEnv,
  Subject,
  Term,
  agentDetailLink,
  agentLabel,
  declaredFields,
  EXACT_FIELDS,
  machineDetailLink,
  matchesTerms,
  parseQuery,
  repositoryDetailLink,
  taskDetailLink,
  worktreeDetailLink,
} from '@cockpit/fleet-data'
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

interface Candidate {
  id: string
  label: string
  detail: string
  link: AppLink
  subject: Subject
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
    results: matching.slice(0, RESULTS_PER_KIND).map(({ candidate }) => ({ id: `${kind}:${candidate.id}`, kind, label: candidate.label, detail: candidate.detail, link: candidate.link })),
    more: Math.max(0, matching.length - RESULTS_PER_KIND),
  }
}

const rows = <T>(source: readonly ListRow<T>[], describe: (row: ListRow<T>) => Omit<Candidate, 'id' | 'subject'>, alsoBare: (row: ListRow<T>) => string[] = () => []): Candidate[] =>
  source.map((row) => ({ id: row.id, subject: { ...row.subject, bare: [...row.subject.bare, ...alsoBare(row)] }, ...describe(row) }))

/**
 * The palette's results for `text`, grouped by kind with at most 8 each: the
 * entities of the fleet document (the branches are those of the worktrees it
 * lists; the lazily loaded branches of a repository page are not searched),
 * matched with the same matcher as the list filters. No text gives no groups.
 */
export function searchPalette(model: FleetModel, text: string, now: number): PaletteGroup[] {
  const terms = parseQuery(text)
  if (terms.length === 0) return []
  const worktreeRows = model.worktreeRows
  const branches: Candidate[] = []
  const seen = new Set<string>()
  for (const row of worktreeRows) {
    const worktree = row.item
    const key = `${worktree.repository}|${worktree.branch}`
    if (worktree.branch === '' || seen.has(key)) continue
    seen.add(key)
    const repository = model.repositoryName(worktree.repository)
    branches.push({
      id: key,
      label: worktree.branch,
      detail: `${repository} · ${worktree.task}`,
      link: worktreeDetailLink(worktree.id),
      subject: {
        bare: [worktree.branch.toLowerCase(), repository.toLowerCase()],
        fields: { branch: [worktree.branch.toLowerCase()], repo: [repository.toLowerCase()], task: [worktree.task.toLowerCase()] },
        ...(row.subject.activityAt === undefined ? {} : { activityAt: row.subject.activityAt }),
      },
    })
  }
  const groups = [
    group(
      'task',
      rows(model.taskRows, (row) => ({
        label: row.item.name,
        detail: [row.item.stateInfo.label, ...row.item.repositories.slice(0, 2)].join(' · '),
        link: taskDetailLink(row.item.name),
      })),
      'tasks',
      terms,
      now,
    ),
    group(
      'repository',
      rows(model.repositoryRows, (row) => ({
        label: row.item.slug,
        detail: [...new Set(row.item.checkouts.map((checkout) => checkout.machine))].join(', '),
        link: repositoryDetailLink(row.item.host, row.item.slug),
      })),
      'repositories',
      terms,
      now,
    ),
    group(
      'worktree',
      rows(worktreeRows, (row) => ({
        label: row.item.task,
        detail: `${row.item.branch} · ${model.repositoryName(row.item.repository)}`,
        link: worktreeDetailLink(row.item.id),
      })),
      'worktrees',
      terms,
      now,
    ),
    group('branch', branches, 'branch', terms, now),
    group(
      'agent',
      rows(model.agentRows, (row) => ({
        label: agentLabel(row.item),
        detail: [model.tasksOfAgent(row.item)[0], row.item.repository === undefined ? undefined : model.repositoryName(row.item.repository), row.item.machine].filter((part) => part).join(' · '),
        link: agentDetailLink(row.item.id),
      }),
      // An agent is also found by its session or run id, which is what the operator holds.
      (row) => [row.item.session_id, row.item.run_id].filter((id): id is string => id !== undefined).map((id) => id.toLowerCase()),
    ),
      'agents',
      terms,
      now,
    ),
    group(
      'machine',
      rows(model.machineRows, (row) => ({
        label: row.item.machine.machine,
        detail: row.item.local ? `${row.item.state}, this machine` : row.item.state,
        link: machineDetailLink(row.item.machine.id),
      })),
      'machines',
      terms,
      now,
    ),
  ]
  return groups.filter((candidate): candidate is PaletteGroup => candidate !== undefined)
}
