import { TestBed } from '@angular/core/testing'
import { CopyCommand, FleetModel } from '@cockpit/fleet-data'
import { branchCleanup, pickRepositories, pullRequestCreate, worktreeCreate, worktreeList } from '@cockpit/fleet-data/commands'
import { PanelCommand, buildAgentPanel, buildPullRequestPanel, buildRepositoryPanel, buildWorktreePanel } from '@cockpit/fleet-data/panel'
import { agent, machine, pullRequest, repository, run, worktree } from '@cockpit/fleet-data/testing'
import { ClipboardWriter } from './clipboard'
import { CopyCommandList, QUOTE_TWICE_HINT, commandSegments, runLocation } from './copy-command-list'
import { StatusAnnouncer } from './status-announcer'

const NOW = Date.parse('2026-10-01T10:00:00Z')

function modelOf(): FleetModel {
  const vmWorktree = { ...worktree('w3', 'r3', 'vm'), route: 'cached' as const, task: 'fix-ci', branch: 'task/fix-ci' }
  return new FleetModel(
    {
      schema_version: 2,
      warming_up: false,
      repositories_total: 3,
      repositories_scanned: 3,
      diagnostics: 0,
      machines: [machine('alpha'), machine('vm', 'cached')],
      repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' }), repository('r3', 'vm', { name: 'sneat-dev/wb', route: 'cached' })],
      worktrees: [{ ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'task/fix-ci' }, vmWorktree],
      pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 12 })],
      agents: [run('run-1', 'running', { task: 'fix-ci', worktrees: ['w1'], repository: 'r1' }), agent('s1', 'r1', 'live', { runtime: 'claude' })],
    },
    { now: () => NOW },
  )
}

async function render(entries: readonly PanelCommand[]) {
  TestBed.resetTestingModule()
  const copy = vi.fn().mockResolvedValue(true)
  TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }] })
  const fixture = TestBed.createComponent(CopyCommandList)
  fixture.componentRef.setInput('entries', entries)
  const copied: string[] = []
  fixture.componentInstance.copied.subscribe((text) => copied.push(text))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, copy, copied, items: [...root.querySelectorAll('li')] }
}

const text = (element: Element | null | undefined) => element?.textContent?.replace(/\s+/g, ' ').trim()

describe('CopyCommandList', () => {
  // cockpit-views#ac:copy-command-uses-only-existing-commands-and-identifiers
  it('copies exactly the library commands of each entity, anonymous, with the run location of each', async () => {
    const model = modelOf()
    const panels: [string, PanelCommand[]][] = [
      ['worktree', buildWorktreePanel(model, 'w1')?.commands ?? []],
      ['pull request', buildPullRequestPanel(model, 'p1')?.commands ?? []],
      ['repository', buildRepositoryPanel(model, 'sneat-dev/wb')?.commands ?? []],
      ['dispatched run', buildAgentPanel(model, 'run-1')?.commands ?? []],
      ['worktree on vm', buildWorktreePanel(model, 'w3')?.commands ?? []],
    ]
    for (const [name, commands] of panels) {
      expect(commands.length, name).toBeGreaterThan(0)
      const { items, copy, copied } = await render(commands)
      expect(items, name).toHaveLength(commands.length)
      for (const [index, item] of items.entries()) {
        const command = commands[index].command as Extract<CopyCommand, { ok: true }>
        expect(text(item.querySelector('code')), name).toBe(command.text)
        ;(item.querySelector('button') as HTMLButtonElement).click()
        await vi.waitFor(() => expect(copy).toHaveBeenLastCalledWith(command.text))
        // The raw text goes to the clipboard: no trailing space or newline, no leading one.
        expect(copy.mock.lastCall?.[0]).not.toMatch(/^\s|\s$/)
        expect(command.text, name).not.toMatch(/--apply|\/Users\/|\/home\//)
      }
      expect(copied.length, name).toBe(commands.length)
    }
    // The worktree on vm is labelled, a local one says "run here".
    const onVm = await render(buildWorktreePanel(model, 'w3')?.commands ?? [])
    expect(text(onVm.items[0].querySelector('.where'))).toBe('run on vm')
    expect(onVm.items[0].querySelector('.where')?.classList.contains('elsewhere')).toBe(true)
    const here = await render(buildWorktreePanel(model, 'w1')?.commands ?? [])
    expect(text(here.items[0].querySelector('.where'))).toBe('run here')
  })

  it('renders nothing for an entity with no command, leaving no box', async () => {
    const { root, items } = await render([])
    expect(items).toEqual([])
    expect(root.querySelector('section, h3')).toBeNull()
  })

  it('marks a command that needs editing: a "Copy template" button, a longer announcement and the library\'s placeholders marked, and nothing else', async () => {
    const create = pullRequestCreate('fix-ci', {})
    const plain = worktreeList('fix-ci')
    const { items } = await render([
      { title: 'Commit and open pull request', command: create },
      { title: 'List worktrees', command: plain },
    ])
    expect(text(items[0].querySelector('.edit'))).toBe('edit before running')
    expect(items[0].querySelector('mark')?.textContent).toBe('<<<edit:message>>>')
    expect(text(items[0].querySelector('code'))).toBe("wb pr create 'fix-ci' --commit-all --message=<<<edit:message>>>")
    expect(text(items[0].querySelector('app-copy-button button'))).toBe('Copy template')
    expect(items[0].querySelector('app-copy-button button')?.getAttribute('aria-label')).toBe('Copy template wb pr create: Commit and open pull request')
    expect(items[0].querySelector('[role="status"]')).toBeNull()
    expect(items[1].querySelector('.edit')).toBeNull()
    expect(items[1].querySelector('mark')).toBeNull()
    expect(text(items[1].querySelector('app-copy-button button'))).toBe('Copy')
    expect(items[1].querySelector('app-copy-button button')?.getAttribute('aria-label')).toBe('Copy wb worktree list: List worktrees')
  })

  it('announces that the parts are to be edited once a template is copied', async () => {
    const { fixture, items } = await render([{ title: 'Commit and open pull request', command: pullRequestCreate('fix-ci', {}) }])
    ;(items[0].querySelector('app-copy-button button') as HTMLButtonElement).click()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(TestBed.inject(StatusAnnouncer).message()).toBe('Copied; edit the <…> parts before running')
    })
  })

  it('marks only the placeholder values the library writes, not any <word> that is part of a quoted value', async () => {
    const { items } = await render([{ title: 'List worktrees', command: worktreeList('<script>') }, { title: 'Create', command: pullRequestCreate('<<<edit:message>>>', {}) }])
    expect(text(items[0].querySelector('code'))).toBe("wb worktree list '<script>'")
    expect(items[0].querySelector('mark')).toBeNull()
    expect(items[0].querySelector('.edit')).toBeNull()
    // A value that spells a placeholder is the library's placeholder: it is bare, and flagged.
    expect(items[1].querySelector('mark')?.textContent).toBe('<<<edit:message>>>')
    expect(commandSegments("wb x '<<<edit:model>>>'")).toEqual([{ text: "wb x '", placeholder: false }, { text: '<<<edit:model>>>', placeholder: true }, { text: "'", placeholder: false }])
  })

  // cockpit-views#ac:copy-command-refuses-hostile-values
  it('shows the reason for a command the library refused, with no copy button and nothing copyable', async () => {
    const hostile: PanelCommand[] = [
      { title: 'List worktrees', command: worktreeList('-x') },
      { title: 'List worktrees', command: worktreeList('a\nb') },
      { title: 'List worktrees', command: worktreeList('a​b') },
      { title: 'Plan branch cleanup', command: branchCleanup('acme/r', '--upstream') },
    ]
    const { items, copy } = await render(hostile)
    for (const item of items) {
      expect(item.querySelector('button')).toBeNull()
      expect(item.querySelector('code')).toBeNull()
      expect(text(item.querySelector('.refused'))).toMatch(/^Not copied: .+/)
    }
    expect(text(items[0].querySelector('.refused'))).toContain('starts with "-"')
    expect(text(items[1].querySelector('.refused'))).toContain('control character')
    expect(copy).not.toHaveBeenCalled()
  })

  // cockpit-views#ac:copy-command-for-an-ssh-machine
  it('says beside an ssh command with a placeholder that the typed text is quoted twice, and beside nothing else', async () => {
    const ssh = { ssh: { host: 'vm.example', user: 'alex' } }
    const { items } = await render([
      { title: 'Create', command: worktreeCreate('t', ['o/r'], {}, ssh) },
      { title: 'List', command: worktreeList('t', ssh) },
      { title: 'Here', command: worktreeCreate('t', ['o/r']) },
    ])
    expect(text(items[0].querySelector('.hint'))).toBe(QUOTE_TWICE_HINT)
    expect(items[1].querySelector('.hint')).toBeNull()
    expect(items[2].querySelector('.hint')).toBeNull()
  })

  it('copies a value with a quote single-quoted, as the library wrote it', async () => {
    const { items, copy } = await render([{ title: 'List worktrees', command: worktreeList("a'; rm -rf ~; '") }])
    expect(text(items[0].querySelector('code'))).toBe(`wb worktree list 'a'\\''; rm -rf ~; '\\'''`)
    ;(items[0].querySelector('button') as HTMLButtonElement).click()
    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith(`wb worktree list 'a'\\''; rm -rf ~; '\\'''`))
  })

  it('names each copy button after its entry, and each command line is focusable for keyboard selection', async () => {
    const { items } = await render(buildPullRequestPanel(modelOf(), 'p1')?.commands ?? [])
    expect(items[0].querySelector('button')?.getAttribute('aria-label')).toBe(`Copy wb pr land: ${buildPullRequestPanel(modelOf(), 'p1')?.commands[0].title}`)
    expect(items[0].querySelector('code')?.getAttribute('tabindex')).toBe('0')
  })

  it('has the picker offering only safe repository names (the library decides)', () => {
    expect(pickRepositories(['owner/na me', 'owner/ok.name'], '', NOW)).toEqual(['owner/ok.name'])
  })

  it('cuts a command at its placeholders and joins back to the same text', () => {
    const original = "wb worktree create <<<edit:task>>> 'a/b' --model=<<<edit:model>>> --original-prompt-file=<<<edit:file>>>"
    const segments = commandSegments(original)
    expect(segments.map((segment) => segment.text).join('')).toBe(original)
    expect(segments.filter((segment) => segment.placeholder).map((segment) => segment.text)).toEqual(['<<<edit:task>>>', '<<<edit:model>>>', '<<<edit:file>>>'])
    expect(commandSegments('wb fleet status')).toEqual([{ text: 'wb fleet status', placeholder: false }])
    expect(commandSegments('<<<edit:message>>>')).toEqual([{ text: '<<<edit:message>>>', placeholder: true }])
  })

  it('says where a command runs', () => {
    expect(runLocation({ ok: true, text: 'wb x', needsEdit: false })).toBe('run here')
    expect(runLocation({ ok: true, text: 'wb x', needsEdit: false, label: 'run on vm' })).toBe('run on vm')
  })
})
