import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { Session } from '@cockpit/fleet-data'
import { MetricsPoller } from '../../metrics/metrics-poller'
import { metricsFetch, sample } from '../machines/machines-fixture'
import { SESSION, openPage } from '../test-harness'
import { OWNER, loadAnswers, newTaskDocument } from './new-task-fixture'
import { NewTaskPage } from './new-task-page'

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

async function open(url = '/tasks/new', session: Session = SESSION, fetcher: typeof fetch = metricsFetch(loadAnswers())) {
  const page = await openPage(url, NewTaskPage, newTaskDocument(), session, fetcher)
  const { root, harness } = page
  const settle = () => harness.fixture.whenStable()
  const fill = async (selector: string, value: string) => {
    const field = root.querySelector(selector) as HTMLInputElement | HTMLTextAreaElement
    field.value = value
    field.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
  }
  const click = async (element: Element | null) => {
    ;(element as HTMLElement).click()
    await settle()
  }
  const commands = () => [...root.querySelectorAll('app-copy-command-list li')].map((item) => ({ title: text(item.querySelector('.title')), where: text(item.querySelector('.where')), edit: text(item.querySelector('.edit')), code: text(item.querySelector('code')), refused: text(item.querySelector('.refused')), button: text(item.querySelector('app-copy-button button')) }))
  const url$ = () => TestBed.inject(Router).url
  const query = () => new URLSearchParams(url$().split('?')[1] ?? '')
  /** Types the repository filter, adds every match, and fills the rest of the AC's form. */
  const fillAll = async () => {
    await fill('app-repository-picker input', 'sneat-*/*-go')
    await click(root.querySelector('.add-all'))
    await fill('#new-task-name', 'fix-ci')
    await fill('#new-task-brief', 'Fix the flaky CI.')
    await fill('#new-task-base', 'main')
    await fill('#new-task-model', 'opus')
  }
  return { ...page, settle, fill, click, commands, query, url: url$, fillAll }
}

describe('NewTaskPage', () => {
  it('renders on its own over an empty fleet, offering this machine and refusing to produce a command', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(NewTaskPage)
    await fixture.whenStable()
    expect(text(fixture.nativeElement.querySelector('.refused'))).toContain('name the task')
    expect(text(fixture.nativeElement.querySelector('.machine'))).toContain('This machine')
  })

  it('shows its title and says that nothing here runs anything', async () => {
    const { root } = await open()
    expect(text(root.querySelector('h2'))).toBe('New task')
    expect(text(root.querySelector('.lead'))).toContain('Nothing here runs anything')
    expect(root.querySelector('form')?.getAttribute('aria-label')).toBe('New task')
  })

  // cockpit-views#ac:new-task-form-produces-commands
  it('produces the creation command for both repositories and one dispatch command each, flagged as needing an edit, and runs nothing', async () => {
    const requested: string[] = []
    const base = metricsFetch(loadAnswers())
    const { commands, fillAll, root } = await open('/tasks/new', SESSION, (async (input: RequestInfo | URL, init?: RequestInit) => {
      requested.push(String(input))
      return base(input, init)
    }) as typeof fetch)
    await fillAll()
    expect([...root.querySelectorAll('.chip-name')].map(text)).toEqual(['sneat-co/sneat-go', 'sneat-co/bots-go'])
    expect(commands()).toEqual([
      {
        title: 'Create the worktrees',
        where: 'run here',
        edit: 'edit before running',
        code: "wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>> --base='main'",
        refused: '',
        button: 'Copy template',
      },
      {
        title: 'Dispatch an agent in sneat-co/sneat-go',
        where: 'run here',
        edit: 'edit before running',
        code: "wb agent dispatch --repo='sneat-co/sneat-go' --task='Fix the flaky CI.' --profile=<<<edit:profile>>> --new-worktree='fix-ci' --base='main'",
        refused: '',
        button: 'Copy template',
      },
      {
        title: 'Dispatch an agent in sneat-co/bots-go',
        where: 'run here',
        edit: 'edit before running',
        code: "wb agent dispatch --repo='sneat-co/bots-go' --task='Fix the flaky CI.' --profile=<<<edit:profile>>> --new-worktree='fix-ci' --base='main'",
        refused: '',
        button: 'Copy template',
      },
    ])
    // Only the machine metrics were read: the form sends nothing and starts nothing.
    expect(requested.every((url) => url.startsWith('/api/v1/cockpit/machine-metrics?machine='))).toBe(true)
  })

  // cockpit-views#ac:new-task-form-produces-commands
  it('refuses to produce a command until a model is entered, saying why', async () => {
    const { fillAll, fill, commands } = await open()
    await fillAll()
    await fill('#new-task-model', '')
    expect(commands()).toEqual([expect.objectContaining({ title: 'Commands', refused: expect.stringContaining('a model is required'), button: '' })])
    await fill('#new-task-model', 'unknown')
    expect(commands()[0].code).toContain("--model='unknown'")
  })

  it('says what is missing: a repository, then the task name', async () => {
    const { fill, click, root, commands } = await open('/tasks/new?model=opus')
    expect(commands()[0].refused).toContain('name the task')
    await fill('#new-task-name', 'solo-task')
    expect(commands()[0].refused).toContain('choose at least one repository')
    await fill('app-repository-picker input', 'web')
    await click(root.querySelector('[role=option]'))
    expect(commands()[0].code).toContain("wb worktree create 'solo-task' 'acme/web'")
  })

  it('writes the form into the address, but not the brief, and restores it from that address', async () => {
    const { fillAll, query, url } = await open()
    await fillAll()
    expect(query().getAll('repo')).toEqual(['sneat-co/sneat-go', 'sneat-co/bots-go'])
    expect([query().get('task'), query().get('base'), query().get('model'), query().has('machine')]).toEqual(['fix-ci', 'main', 'opus', false])
    expect(url()).not.toContain('flaky')
    const again = await open(url())
    expect(again.commands()[0].code).toBe("wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>> --base='main'")
    // The brief did not travel: the dispatch asks for it.
    expect(again.commands()[1].code).toContain('--task=<<<edit:brief>>>')
    expect((again.root.querySelector('#new-task-brief') as HTMLTextAreaElement).value).toBe('')
    expect((again.root.querySelector('#new-task-name') as HTMLInputElement).value).toBe('fix-ci')
  })

  it('removes a repository from the form and from the address', async () => {
    const { root, click, query, commands } = await open('/tasks/new?repo=sneat-co/sneat-go&repo=sneat-co/bots-go&task=t&model=opus')
    await click(root.querySelector('button[aria-label="Remove sneat-co/sneat-go"]'))
    expect(query().getAll('repo')).toEqual(['sneat-co/bots-go'])
    expect(commands()).toHaveLength(2)
    expect(commands()[0].code).toBe("wb worktree create 't' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>>")
  })

  it('adds a repository once, even if it is added again', async () => {
    const { root, fill, click, query } = await open('/tasks/new?repo=acme/web')
    await fill('app-repository-picker input', 'acme')
    await click(root.querySelector('[role=option]'))
    expect(query().getAll('repo')).toEqual(['acme/web', 'acme/tools'])
  })

  it('follows back and forward, and a new address from outside (the top bar button) clears the form', async () => {
    const { fillAll, harness, settle, root } = await open()
    await fillAll()
    const router = TestBed.inject(Router)
    await router.navigateByUrl('/tasks/new?task=other&model=haiku')
    await settle()
    expect((root.querySelector('#new-task-name') as HTMLInputElement).value).toBe('other')
    expect((root.querySelector('#new-task-model') as HTMLInputElement).value).toBe('haiku')
    expect(root.querySelectorAll('.chip')).toHaveLength(0)
    await router.navigateByUrl('/tasks/new')
    await settle()
    expect((root.querySelector('#new-task-name') as HTMLInputElement).value).toBe('')
    harness.detectChanges()
    expect(text(root.querySelector('.chosen'))).toBe('None chosen yet')
  })

  it('does not take its own edits for news while the router is still reporting an earlier one', async () => {
    const { root, settle } = await open()
    const field = root.querySelector('#new-task-name') as HTMLInputElement
    for (const value of ['a', 'ab', 'abc']) {
      field.value = value
      field.dispatchEvent(new Event('input', { bubbles: true }))
    }
    await settle()
    expect(field.value).toBe('abc')
    expect(TestBed.inject(Router).url).toContain('task=abc')
  })

  it('does not let the address of an earlier edit undo a later one', async () => {
    const { root, settle } = await open()
    const field = root.querySelector('#new-task-name') as HTMLInputElement
    for (const value of ['a', 'ab']) {
      field.value = value
      field.dispatchEvent(new Event('input', { bubbles: true }))
    }
    await TestBed.inject(Router).navigateByUrl('/tasks/new?task=a')
    await settle()
    expect(field.value).toBe('ab')
  })

  describe('the task name', () => {
    it('says the safe set while it is empty or good, and refuses a name outside it', async () => {
      const { root, fill, commands } = await open('/tasks/new?repo=acme/web&model=opus')
      expect(text(root.querySelector('#new-task-name-hint'))).toBe('The worktree and the branch name: letters, digits, dots, underscores and hyphens.')
      await fill('#new-task-name', 'bad name')
      expect(text(root.querySelector('#new-task-name-hint'))).toBe('a task name is made of letters, digits, dots, underscores and hyphens')
      expect(root.querySelector('#new-task-name-hint')?.getAttribute('role')).toBe('alert')
      expect(root.querySelector('#new-task-name')?.getAttribute('aria-invalid')).toBe('true')
      expect(commands()[0].refused).toContain('a task name is made of')
      await fill('#new-task-name', '-rf')
      expect(text(root.querySelector('#new-task-name-hint'))).toContain('starts with "-"')
    })

    it('tells, as you type, that a task of that name already exists, and links to it', async () => {
      const { root, fill } = await open('/tasks/new?repo=acme/web&model=opus')
      await fill('#new-task-name', 'fix-ci')
      expect(text(root.querySelector('#new-task-name-hint'))).toBe('A task of this name exists, with 2 worktrees: open it. The commands add to it.')
      expect(root.querySelector('#new-task-name-hint a')?.getAttribute('href')).toBe('/tasks/detail?task=fix-ci')
      await fill('#new-task-name', 'solo')
      expect(text(root.querySelector('#new-task-name-hint'))).toContain('with 1 worktree:')
      expect(root.querySelector('#new-task-name-hint')?.getAttribute('role')).toBeNull()
      await fill('#new-task-name', 'fresh')
      expect(root.querySelector('#new-task-name-hint a')).toBeNull()
    })
  })

  describe('the base branch and the model', () => {
    it('shows the default branch of what is chosen as the placeholder, and says each repository\'s when they differ', async () => {
      const { root, fill, click } = await open()
      const base = () => root.querySelector('#new-task-base') as HTMLInputElement
      expect(base().placeholder).toBe('the default branch')
      expect(text(base().nextElementSibling)).toBe("Left empty it is each repository's default branch.")
      await fill('app-repository-picker input', 'sneat-go')
      await click(root.querySelector('[role=option]'))
      expect(base().placeholder).toBe('main')
      expect(text(base().nextElementSibling)).toBe('Left empty it is main, the default branch.')
      await fill('app-repository-picker input', 'tools')
      await click(root.querySelector('[role=option]'))
      expect(base().placeholder).toBe('the default branch')
    })

    it('offers the known models and the fleet\'s own as a list, and accepts any text', async () => {
      const { root, fill, commands } = await open('/tasks/new?repo=acme/web&task=t')
      expect([...root.querySelectorAll('#new-task-models option')].map((option) => (option as HTMLOptionElement).value)).toEqual(['opus', 'sonnet', 'haiku', 'codex', 'gemini', 'unknown', 'sonnet-5-5', 'gpt-x'])
      expect(root.querySelector('#new-task-model')?.getAttribute('list')).toBe('new-task-models')
      expect(root.querySelector('#new-task-model')?.hasAttribute('required')).toBe(true)
      await fill('#new-task-model', 'my own model 7')
      expect(commands()[0].code).toContain("--model='my own model 7'")
    })
  })

  describe('the brief', () => {
    it('goes to the dispatch command as its --task, with line breaks and quotes in one word, and is kept in memory only', async () => {
      const { root, fill, commands, url } = await open('/tasks/new?repo=acme/web&task=t&model=opus')
      await fill('#new-task-brief', "It's\nmulti-line")
      expect(commands()[1].code).toContain("--task='It'\\''s multi-line'")
      expect(url()).not.toContain('multi')
      expect(text(root.querySelector('#new-task-brief')?.nextElementSibling)).toContain('not put in the address')
    })

    it('shows the library\'s refusal for a control character in the brief, with the other commands still offered', async () => {
      const { fill, commands } = await open('/tasks/new?repo=acme/web&task=t&model=opus')
      await fill('#new-task-brief', 'bad\u0007brief')
      expect(commands()[0].code).toContain('wb worktree create')
      expect(commands()[1].refused).toContain('a value with a control character')
    })
  })

  describe('the machine', () => {
    it('offers this machine only to an anonymous reader, with the load verdict beside it, and says why there is no other', async () => {
      const { root, settle } = await open()
      await vi.waitFor(() => expect(TestBed.inject(MetricsPoller).entries().size).toBeGreaterThan(0))
      await settle()
      expect([...root.querySelectorAll('.machine')].map(text)).toEqual(['This machine (alpha) Load: busy'])
      expect(text(root.querySelector('.machines .hint'))).toContain('owner session')
      expect((root.querySelector('input[type=radio]') as HTMLInputElement).checked).toBe(true)
    })

    it('offers a machine with an SSH route to an owner session, with its load, and writes the commands through ssh when it is chosen', async () => {
      const { root, click, settle, commands, query } = await open('/tasks/new?repo=acme/web&task=t&model=opus', OWNER)
      await vi.waitFor(() => expect(TestBed.inject(MetricsPoller).entries().size).toBe(2))
      await settle()
      expect([...root.querySelectorAll('.machine')].map(text)).toEqual(['This machine (alpha) Load: busy', 'beta Load: free'])
      expect(root.querySelector('.machines .hint')).toBeNull()
      await click(root.querySelectorAll('input[type=radio]')[1])
      expect(query().get('machine')).toBe('mach-beta')
      expect(commands()[0].code).toBe("ssh me@beta.example wb worktree create 't' 'acme/web' --model='opus' --original-prompt-file=<<<edit:file>>>")
      expect(commands()[0].where).toBe('run here')
      await click(root.querySelectorAll('input[type=radio]')[0])
      expect(query().has('machine')).toBe(false)
      expect(commands()[0].code).toMatch(/^wb worktree create/)
    })

    // cockpit-views#ac:in-flight-machine-load-indicator
    it('says load unknown, with the age of the sample, for a machine whose sample is not current, never "free"', async () => {
      const answers = { ...loadAnswers(), 'mach-beta': { machine: 'mach-beta', route: 'cached' as const, samples: [sample(120, 10)] } }
      const { root, settle } = await open('/tasks/new', OWNER, metricsFetch(answers))
      await vi.waitFor(() => expect(TestBed.inject(MetricsPoller).entries().size).toBe(2))
      await settle()
      expect([...root.querySelectorAll('.machine')].map(text)).toEqual(['This machine (alpha) Load: busy', 'beta Load: load unknown cached, 2 h ago: too old to say'])
    })

    it('says load unknown for a machine that has no metrics answer, and never "free" by default', async () => {
      const { root, settle } = await open('/tasks/new', OWNER, (async () => new Response('{}', { status: 500 })) as typeof fetch)
      await settle()
      expect([...root.querySelectorAll('.machine')].map(text)).toEqual(['This machine (alpha) Load: load unknown', 'beta Load: load unknown'])
    })

    it('falls back to this machine for a machine in the address that cannot be chosen', async () => {
      const { root, commands } = await open('/tasks/new?repo=acme/web&task=t&model=opus&machine=mach-beta')
      expect((root.querySelector('input[type=radio]') as HTMLInputElement).checked).toBe(true)
      expect(commands()[0].code).toMatch(/^wb worktree create/)
    })
  })

  it('polls only the machines that can be chosen, and none when the document does not list this machine', async () => {
    const requested: string[] = []
    await open('/tasks/new', OWNER, (async (input: RequestInfo | URL) => {
      requested.push(String(input))
      return new Response(JSON.stringify({ machine: 'x', route: 'none', samples: [] }), { status: 200 })
    }) as typeof fetch)
    await vi.waitFor(() => expect(requested.length).toBeGreaterThanOrEqual(2))
    expect(requested.slice(0, 2).sort()).toEqual(['/api/v1/cockpit/machine-metrics?machine=mach-alpha', '/api/v1/cockpit/machine-metrics?machine=mach-beta'])
  })
})
