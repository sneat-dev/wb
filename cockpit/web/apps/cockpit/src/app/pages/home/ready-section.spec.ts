import { TestBed } from '@angular/core/testing'
import { registryAction } from '@cockpit/fleet-data/testing'
import { ClipboardWriter } from '@cockpit/ui/control'
import { FIXED_CLOCK, fleet, modelOf, only } from './home-testing'
import { HomeRegistry, LAND_ACTION } from './home-registry'
import { ReadySection, throttleNote } from './ready-section'

async function render(document = fleet(), options: { warming?: boolean; offered?: string[] } = {}) {
  const registry = { offered: (target: string) => (options.offered?.includes(target) ? [registryAction(LAND_ACTION, 'Land', { target_types: ['pull_request'] })] : undefined) }
  const copy = vi.fn().mockResolvedValue(true)
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [FIXED_CLOCK, { provide: HomeRegistry, useValue: registry }, { provide: ClipboardWriter, useValue: { copy } }] })
  const fixture = TestBed.createComponent(ReadySection)
  const model = modelOf(document)
  fixture.componentRef.setInput('model', model)
  fixture.componentRef.setInput('warming', options.warming ?? false)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, model, copy, rows: [...root.querySelectorAll<HTMLElement>('.home-row')] }
}

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('ReadySection', () => {
  it('lists each ready task with its repositories, checks and the age of the oldest observation', async () => {
    const { root, rows } = await render()
    expect(text(root.querySelector('h2'))).toBe('Ready to land 2')
    expect(rows).toHaveLength(3)
    expect(text(rows[0].querySelector('.home-task'))).toBe('improve-docs')
    expect(text(rows[0].querySelector('.home-reason'))).toBe('2 repositories · 10/10 checks · checked 9 min ago')
    expect(text(rows[1].querySelector('.home-reason'))).toBe('1 repository · 5/5 checks · checked 6 min ago')
    expect(text(rows[0].querySelector('app-state-badge'))).toContain('ready to land')
  })

  it('has one pull request line per pull request, each linking out safely, with "Copy command" for `wb pr land` where there is no registry', async () => {
    const { fixture, rows, copy } = await render()
    const lines = [...rows[0].querySelectorAll('.home-pr')]
    expect(lines.map((line) => text(line.querySelector('.home-pr-number')))).toEqual(['#12', '#7'])
    expect(lines.map((line) => text(line.querySelector('.home-place')))).toEqual(['sneat-dev/wb', 'sneat-co/sneat-go'])
    const link = lines[0].querySelector('a.home-pr-number') as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('https://github.com/example/r-wb/pull/12')
    expect(link.getAttribute('rel')).toBe('noopener noreferrer')
    expect(link.getAttribute('aria-label')).toBe('Pull request 12, opens in a new tab')
    const buttons = lines.map((line) => line.querySelector('button') as HTMLButtonElement)
    expect(buttons.map((button) => button.getAttribute('aria-label'))).toEqual(['Copy command: wb pr land sneat-dev/wb#12', 'Copy command: wb pr land sneat-co/sneat-go#7'])
    buttons[0].click()
    await fixture.whenStable()
    expect(copy).toHaveBeenCalledWith("wb pr land 'sneat-dev/wb#12'")
    expect(rows[0].querySelectorAll('app-action-slot')).toHaveLength(0)
  })

  it("puts the registry's landing action in a slot for each pull request that has one", async () => {
    const { rows } = await render(fleet(), { offered: ['pull_request:pr-r1'] })
    const lines = [...rows[0].querySelectorAll('.home-pr')]
    expect(lines[0].querySelector('app-action-slot button')?.textContent?.trim()).toBe('Land')
    expect(lines[0].querySelector('app-copy-button')).toBeNull()
    expect(lines[1].querySelector('app-action-slot')).toBeNull()
    expect(lines[1].querySelector('app-copy-button')).not.toBeNull()
  })

  it('lists the tasks that wait on checks below, muted, with how long ago the checks were read and no action', async () => {
    const { rows } = await render()
    expect(rows[2].classList.contains('muted-row')).toBe(true)
    expect(text(rows[2])).toContain('add-telemetrywaiting on checks · checked 4 min ago')
    expect(rows[2].querySelectorAll('a, button')).toHaveLength(0)
  })

  it('says a task another machine reported, names the machine of its pull requests and offers nothing for them', async () => {
    const document = only('improve-docs')
    for (const entry of [...document.worktrees, ...document.pull_requests]) Object.assign(entry, { machine: 'vm', machine_id: 'mach-vm', route: 'live-remote' })
    const { rows } = await render(document, { offered: ['pull_request:pr-r1'] })
    expect(text(rows[0].querySelector('.home-reason'))).toMatch(/^as reported by vm · /)
    const line = rows[0].querySelector('.home-pr') as HTMLElement
    expect(text(line)).toContain('vm')
    expect(line.querySelector('app-action-slot, button')).toBeNull()
  })

  it('says it has nothing ready in one calm line, or a skeleton while the daemon is still scanning', async () => {
    const calm = await render(fleet('healthy'))
    expect(text(calm.root.querySelector('.home-calm'))).toBe('Nothing is ready to land.')
    expect(calm.root.querySelector('.home-count')).toBeNull()
    const warming = await render(fleet('warming'), { warming: true })
    expect(warming.root.querySelector('.home-calm')).toBeNull()
    expect(warming.root.querySelector('app-skeleton-rows')).not.toBeNull()
  })

  it('names a pull request with no checks counted, and one with no address, plainly', async () => {
    const document = only('speed-up-index')
    Object.assign(document.pull_requests[0], { checks_passed: undefined, checks_total: undefined, url: undefined })
    const { rows } = await render(document)
    expect(text(rows[0].querySelector('.home-reason'))).toContain('checks not reported')
    expect(rows[0].querySelector('a.home-pr-number')).toBeNull()
    expect(text(rows[0].querySelector('.home-pr-number'))).toBe('#41')
  })

  describe('the note about throttled pull request state', () => {
    it("says how old the state may be when the daemon's hourly budget cut the pass short", async () => {
      const document = fleet()
      Object.assign(document, { pull_requests_throttled: true })
      const { root } = await render(document)
      expect(text(root.querySelector('.home-note'))).toBe('PR state may be up to 9 min old')
    })

    it('says nothing when the pass was not throttled, and is vague when no observation has a time', async () => {
      const { model } = await render()
      expect(throttleNote(model, model.readyToLand)).toBeUndefined()
      const throttled = fleet()
      Object.assign(throttled, { pull_requests_throttled: true })
      const rows = modelOf(throttled)
      expect(throttleNote(rows, { ready: [], notReady: [] })).toBe('PR state may be out of date')
    })
  })
})
