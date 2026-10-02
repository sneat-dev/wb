import { Type } from '@angular/core'
import { vi } from 'vitest'
import { FleetDocument, Session } from '@cockpit/fleet-data'
import { buildHealth } from '@cockpit/fleet-data/home-details'
import { AgentDetailPage } from './agents/agent-detail-page'
import { AgentsPage } from './agents/agents-page'
import { fleet } from './home/home-testing'
import { HomePage } from './home/home-page'
import { MachineDetailPage } from './machines/machine-detail-page'
import { MachinesPage } from './machines/machines-page'
import { NewTaskPage } from './new-task/new-task-page'
import { RepositoriesPage } from './repositories/repositories-page'
import { RepositoryDetailPage } from './repositories/repository-detail-page'
import { RepositoryPage } from './repositories/repository-page'
import { TaskDetailPage } from './tasks/task-detail-page'
import { TasksPage } from './tasks/tasks-page'
import { openPage, settle } from './test-harness'
import { WorktreePage } from './worktrees/worktree-page'
import { WorktreesPage } from './worktrees/worktrees-page'

// cockpit#req:anonymous-local-reads-metadata-only and cockpit-views#req:owner-gating-is-visible, page by page: an
// anonymous session reads the metadata only. Nothing that needs an owner session is requested, and nothing that
// carries an SSH route (a host, a user, an executable path, an `ssh` command) is rendered, even when the session
// response carries `machine_routes` for a principal that is not the owner (the daemon never sends it, and the
// page must not believe it if it did).

const SECRETS = ['secret-host.example', 'secretuser', '/opt/secret/wb']
const document = (): FleetDocument => fleet('remote-error')

const routesOf = (doc: FleetDocument): Session['machine_routes'] =>
  doc.machines.filter((machine) => machine.route !== 'local').map((machine) => ({ machine_id: machine.id, ssh: { host: SECRETS[0], user: SECRETS[1], wb_path: SECRETS[2] } }))

const sessionOf = (principal: 'anonymous-local' | 'owner', doc: FleetDocument): Session => ({
  principal,
  capabilities: principal === 'owner' ? ['fleet.read', 'repo.content.read'] : ['fleet.read'],
  code_browser_url: 'https://codegrapher.dev/',
  machine_routes: routesOf(doc),
})

/** The routes of the application, each with the component that renders it, over the busy fleet of Home's fixtures. */
const PAGES: { name: string; url: string; page: Type<unknown> }[] = [
  { name: 'Home', url: '/', page: HomePage },
  { name: 'Tasks', url: '/tasks', page: TasksPage },
  { name: 'New task', url: '/tasks/new', page: NewTaskPage },
  { name: 'Task detail', url: '/tasks/detail?task=refactor-cache', page: TaskDetailPage },
  { name: 'Repositories', url: '/repositories', page: RepositoriesPage },
  { name: 'Repository detail', url: '/repositories/github.com/sneat-dev/wb', page: RepositoryDetailPage },
  { name: 'Repository (by id)', url: '/repositories/r-wb', page: RepositoryPage },
  { name: 'Worktrees', url: '/worktrees', page: WorktreesPage },
  { name: 'Worktree detail', url: '/worktrees/wt-1', page: WorktreePage },
  { name: 'Agents', url: '/agents', page: AgentsPage },
  { name: 'Agent detail', url: '/agents/vm-blocked', page: AgentDetailPage },
  { name: 'Machines', url: '/machines', page: MachinesPage },
  { name: 'Machine detail (live over ssh)', url: '/machines/mach-vm', page: MachineDetailPage },
  { name: 'Machine detail (stale)', url: '/machines/mach-old', page: MachineDetailPage },
]

/** A fetch that answers the two lazy metadata routes and records every address asked, so a spec can see what was requested. */
function recordingFetch() {
  const asked: string[] = []
  const fetcher = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    asked.push(url)
    if (url.startsWith('/api/v1/cockpit/machine-metrics')) return new Response(JSON.stringify({ machine: 'x', route: 'none', samples: [] }), { status: 200 })
    if (url.startsWith('/api/v1/cockpit/branches')) return new Response(JSON.stringify({ branches: [] }), { status: 200 })
    return new Response('{}', { status: 404 })
  })
  return { fetcher: fetcher as unknown as typeof fetch, asked }
}

const METADATA_ROUTE = /^\/api\/v1\/cockpit\/(machine-metrics|branches)\?/

async function render(url: string, page: Type<unknown>, principal: 'anonymous-local' | 'owner') {
  const doc = document()
  const { fetcher, asked } = recordingFetch()
  const opened = await openPage(url, page, doc, sessionOf(principal, doc), fetcher)
  await settle(opened.harness.fixture, () => true)
  // The lazy sections of Home and the panels arrive after the first paint.
  await new Promise((resolve) => setTimeout(resolve, 30))
  await opened.harness.fixture.whenStable()
  return { text: opened.root.textContent ?? '', html: opened.root.innerHTML, asked, store: opened.store }
}

describe('owner gating on every page, for an anonymous session', () => {
  it.each(PAGES)('$name: asks only the metadata routes and shows no SSH route', async ({ url, page }) => {
    const { text, html, asked } = await render(url, page, 'anonymous-local')
    expect(text.length).toBeGreaterThan(0)
    // Only the closed list of metadata routes: no README, no action registry, no operation.
    expect(asked.filter((address) => !METADATA_ROUTE.test(address))).toEqual([])
    for (const secret of SECRETS) {
      expect(text).not.toContain(secret)
      expect(html).not.toContain(secret)
    }
    expect(text).not.toMatch(/\bssh [^ ]*@|\bssh secret/)
  })
})

/** Every command text that Home's Fleet health offers to copy, which the rendered page keeps behind its Copy buttons. */
function healthCommands(store: { model: () => ReturnType<typeof import('@cockpit/fleet-data').FleetStore.prototype.model> }): string[] {
  const health = buildHealth(store.model())
  return [...health.staleMachines, ...health.olderWb, ...health.remoteErrors, ...health.exportDropped, ...health.publishErrors].flatMap((item) => ('text' in item.command ? [item.command.text] : []))
}

describe('what a copied command carries', () => {
  it('has no SSH route for an anonymous session, even one whose session response carried machine_routes', async () => {
    const { store } = await render('/', HomePage, 'anonymous-local')
    expect(healthCommands(store).length).toBeGreaterThan(0)
    for (const command of healthCommands(store)) expect(command).not.toMatch(/\bssh\b/)
    expect(store.model().machineRoutes).toBeUndefined()
    expect(store.model().machines.filter((view) => view.machine.route !== 'local').map((view) => store.model().sshRouteFor(view.machine))).toEqual([undefined, undefined])
  })

  // The control: the checks above can fail. An owner session gets the ssh form, run from here.
  it('is the ssh form, run here, for an owner session with a route to the machine', async () => {
    const { store, text } = await render('/', HomePage, 'owner')
    expect(healthCommands(store).filter((command) => command.startsWith(`ssh ${SECRETS[1]}@${SECRETS[0]} `)).length).toBeGreaterThan(0)
    expect(text).toContain('run here')
  })

  it('shows the owner the ssh form of a machine panel command', async () => {
    const { text } = await render('/machines/mach-vm', MachineDetailPage, 'owner')
    expect(text).toContain(`ssh ${SECRETS[1]}@${SECRETS[0]}`)
  })
})
