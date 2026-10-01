import { Component, computed, inject } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { By } from '@angular/platform-browser'
import { Router, provideRouter } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { FleetDocument, FleetStore, ListRow, Worktree } from '@cockpit/fleet-data'
import { fleetDocument, performanceFixture, worktree } from '@cockpit/fleet-data/testing'
import { ListCell, ListPanelTemplate } from './list-cell'
import { LIST_SHORTCUTS, ListFilterTarget } from './list-host'
import { ListChip, ListColumn } from './list-state'
import { SKELETON_ROWS, ListView } from './list-view'

const NOW = Date.parse('2026-10-01T10:05:00Z')
const DAY = 24 * 60 * 60 * 1000
const ago = (days: number) => new Date(NOW - days * DAY).toISOString()

const CHIPS: ListChip[] = [
  { id: 'unpushed', label: 'Unpushed', hint: 'this machine only' },
  { id: 'gone', label: 'Upstream gone' },
  { id: 'pr', label: 'Pull request' },
]

const COLUMNS: ListColumn<Worktree>[] = [
  { id: 'worktree', header: 'Worktree', sort: 'worktree', width: 'fill', grow: 3, min: 100, text: (w) => w.task },
  { id: 'branch', header: 'Branch', width: 'fill', empty: (w) => w.branch === w.task, text: (w) => w.branch, drop: 'phone', hint: 'The branch' },
  { id: 'machine', header: 'Machine', sort: 'machine', width: 90, drop: 'narrow', align: 'end' },
  { id: 'activity', header: 'Last activity', sort: 'activity', width: 100 },
]

@Component({
  imports: [ListView, ListCell, ListPanelTemplate],
  template: `<app-list page="worktrees" noun="worktrees" [rows]="rows()" [columns]="columns" [chips]="chips" [panelLabel]="label">
    <ng-template appCell="worktree" let-w><a class="task" href="/somewhere">{{ w.task }}</a></ng-template>
    <ng-template appCell="branch" let-w>{{ w.branch }}</ng-template>
    <ng-template appCell="machine" let-w><button type="button" class="inner">{{ w.machine }}</button></ng-template>
    <ng-template appListPanel let-w><p class="panel-body">Panel of {{ w.task }}</p></ng-template>
  </app-list>`,
})
class Host {
  protected readonly store = inject(FleetStore)
  protected readonly rows = computed(() => this.store.model().worktreeRows)
  protected readonly columns = COLUMNS
  protected readonly chips = CHIPS
  protected readonly label = (w: Worktree) => `Worktree ${w.task}`
}

@Component({
  imports: [ListView, ListCell],
  template: `<app-list page="worktrees" noun="worktrees" [rows]="rows()" [columns]="columns" [chips]="chips" [resolve]="resolve">
    <ng-template appCell="worktree" let-w>{{ w.task }}</ng-template>
  </app-list>`,
})
class NoPanelHost {
  protected readonly store = inject(FleetStore)
  protected readonly rows = computed(() => this.store.model().worktreeRows)
  protected readonly columns = COLUMNS
  protected readonly chips = CHIPS
  protected readonly resolve = (rows: readonly ListRow<Worktree>[], sel: string) => rows.find((row) => row.item.task === sel)
}

@Component({
  imports: [ListView, ListCell, ListPanelTemplate],
  template: `<app-list page="worktrees" noun="worktrees" [rows]="rows()" [columns]="columns" [chips]="chips">
    <ng-template appCell="worktree" let-w>{{ w.task }}</ng-template>
    <ng-template appListPanel let-w>{{ w.task }}</ng-template>
  </app-list>`,
})
class PlainHost {
  protected readonly store = inject(FleetStore)
  protected readonly rows = computed(() => this.store.model().worktreeRows)
  protected readonly columns = COLUMNS
  protected readonly chips = CHIPS
}

function documentOf(): FleetDocument {
  return fleetDocument({
    worktrees: [
      { ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'task/fix-ci', owner_state: 'active', ahead: 2, last_activity_at: ago(3) },
      { ...worktree('w2', 'r1', 'alpha'), task: 'add-search', branch: 'topic', owner_state: 'idle', last_activity_at: ago(1) },
      { ...worktree('w3', 'r2', 'beta'), task: 'far-task', branch: 'task/far-task', route: 'cached', last_activity_at: ago(45) },
      { ...worktree('w4', 'r1', 'alpha'), task: 'x'.repeat(200), branch: 'b4', last_activity_at: ago(10) },
      { ...worktree('w5', 'r1', 'alpha'), task: 'zeta', branch: 'zeta', upstream_gone: true, last_activity_at: ago(2) },
      { ...worktree('w6', 'r2', 'beta'), task: 'beta-task', branch: 'beta-task', route: 'cached' },
    ],
  })
}
// The default order: newest activity first, none last.
const DEFAULT_ORDER = ['add-search', 'zeta', 'fix-ci', 'x'.repeat(200), 'far-task', 'beta-task']

let filterTarget: ListFilterTarget | undefined
let closePanel: (() => boolean) | undefined
const unregistered = { filter: vi.fn(), panel: vi.fn() }

async function open(url: string, document: FleetDocument = documentOf(), host: typeof Host | typeof NoPanelHost | typeof PlainHost = Host) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [
      provideRouter([{ path: 'list', component: host }]),
      {
        provide: LIST_SHORTCUTS,
        useValue: {
          registerFilter: (target: ListFilterTarget) => {
            filterTarget = target
            return unregistered.filter
          },
          registerPanel: (close: () => boolean) => {
            closePanel = close
            return unregistered.panel
          },
        },
      },
    ],
  })
  const store = TestBed.inject(FleetStore)
  store.document.set(document)
  store.now.set(NOW)
  store.loaded.set(true)
  const harness = await RouterTestingHarness.create()
  await harness.navigateByUrl(url, host)
  await harness.fixture.whenStable()
  const root = harness.routeNativeElement as HTMLElement
  return {
    harness,
    store,
    root,
    router: TestBed.inject(Router),
    list: () => harness.fixture.debugElement.query(By.directive(ListView)).componentInstance as { result: () => unknown },
    viewport: root.querySelector('.viewport') as HTMLElement,
    input: root.querySelector('input') as HTMLInputElement,
    settle: () => harness.fixture.whenStable(),
  }
}

const rowElements = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role=row][aria-rowindex]')]
const tasks = (root: HTMLElement) => rowElements(root).map((row) => row.querySelector('.task')?.textContent)
const frame = () => new Promise((done) => setTimeout(done, 40))
const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const button = (root: HTMLElement, name: string) => [...root.querySelectorAll('button')].find((candidate) => text(candidate) === name) as HTMLButtonElement

function keydown(target: Element, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init })
  target.dispatchEvent(event)
  return event
}

async function type(page: Awaited<ReturnType<typeof open>>, value: string) {
  page.input.value = value
  page.input.dispatchEvent(new Event('input', { bubbles: true }))
  await page.settle()
}

describe('ListView', () => {
  afterEach(() => vi.restoreAllMocks())

  it('lists the rows in the default order under a header, with the count, and one line per row with the full value in its title', async () => {
    const { root } = await open('/list')
    expect(tasks(root)).toEqual(DEFAULT_ORDER)
    expect(text(root.querySelector('.count'))).toBe('6 of 6')
    expect([...root.querySelectorAll('[role=columnheader]')].map(text)).toEqual(['Worktree', 'Branch', 'Machine', 'Last activity'])
    // The long name is truncated by CSS, never wrapped, and its title holds all of it.
    const long = rowElements(root)[3].querySelector('[role=gridcell]') as HTMLElement
    expect(long.getAttribute('title')).toBe('x'.repeat(200))
    expect(rowElements(root)[0].querySelectorAll('[role=gridcell]')[3].hasAttribute('title')).toBe(false)
    expect(root.querySelector('.viewport')?.getAttribute('aria-rowcount')).toBe('7')
    expect(rowElements(root)[0].getAttribute('aria-rowindex')).toBe('2')
    expect(root.querySelector('app-side-panel')).toBeNull()
  })

  it('marks which columns drop first, and how a header reads and sorts', async () => {
    const page = await open('/list')
    const root = page.root
    const headers = [...root.querySelectorAll<HTMLElement>('[role=columnheader]')]
    expect(headers.map((header) => header.getAttribute('aria-sort'))).toEqual(['none', null, 'none', 'descending'])
    expect(headers[1].getAttribute('title')).toBe('The branch')
    expect(headers[1].classList.contains('drop-phone')).toBe(true)
    expect(headers[2].classList.contains('drop-narrow')).toBe(true)
    expect(headers[2].classList.contains('end')).toBe(true)
    expect(headers[0].style.minWidth).toBe('100px')
    expect(headers[3].style.minWidth).toBe('')
    // A fill column shares what is left by its grow; a fixed one shrinks before it would push a fill column under its minimum.
    const list = page.list() as unknown as { style: (column: ListColumn<Worktree>) => string }
    expect(COLUMNS.map((column) => list.style(column))).toEqual(['3 1 0', '1 1 0', '0 1 90px', '0 1 100px'])
  })

  // cockpit-views#ac:filter-state-lives-in-the-address
  it('filters at once while typing, and writes the text to the address within the next frame without a history entry per key', async () => {
    const page = await open('/list')
    const history = vi.spyOn(page.router, 'navigate')
    await type(page, 'fix')
    expect(tasks(page.root)).toEqual(['fix-ci'])
    expect(text(page.root.querySelector('.count'))).toBe('1 of 6')
    expect(page.router.url).toBe('/list')
    await type(page, 'fix-')
    await frame()
    await page.settle()
    expect(page.router.url).toBe('/list?q=fix-')
    expect(history).toHaveBeenCalledTimes(1)
    expect(history.mock.calls[0][1]).toMatchObject({ replaceUrl: true, queryParamsHandling: 'merge' })
  })

  it('toggles a quick filter and a machine, each in the address, and says which are pressed', async () => {
    const page = await open('/list')
    const unpushed = button(page.root, 'Unpushed')
    expect(unpushed.getAttribute('aria-pressed')).toBe('false')
    expect(unpushed.getAttribute('title')).toBe('this machine only')
    expect(button(page.root, 'Pull request').hasAttribute('title')).toBe(false)
    unpushed.click()
    await page.settle()
    expect(page.router.url).toBe('/list?chips=unpushed')
    expect(tasks(page.root)).toEqual(['fix-ci'])
    expect(unpushed.getAttribute('aria-pressed')).toBe('true')
    button(page.root, 'beta').click()
    await page.settle()
    expect(page.router.url).toBe('/list?chips=unpushed&machine=mach-beta')
    expect(tasks(page.root)).toEqual([])
    unpushed.click()
    await page.settle()
    button(page.root, 'beta').click()
    await page.settle()
    expect(page.router.url).toBe('/list')
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
  })

  it('offers the machine chips only when there is more than one machine, or one is selected', async () => {
    const single = { ...documentOf(), machines: [documentOf().machines[0]] }
    const one = await open('/list', single)
    expect(one.root.querySelector('[aria-label=Machines]')).toBeNull()
    const selected = await open('/list?machine=mach-alpha', single)
    expect(selected.root.querySelector('[aria-label=Machines]')).not.toBeNull()
  })

  it('sorts by a header, reverses on the second click, and shows the direction in the header', async () => {
    const page = await open('/list')
    button(page.root, 'Last activity').click()
    await page.settle()
    expect(page.router.url).toBe('/list?sort=activity&dir=asc')
    expect(tasks(page.root)).toEqual(['far-task', 'x'.repeat(200), 'fix-ci', 'zeta', 'add-search', 'beta-task'])
    button(page.root, 'Last activity').click()
    await page.settle()
    expect(page.router.url).toBe('/list?sort=activity&dir=desc')
    button(page.root, 'Machine').click()
    await page.settle()
    expect(page.router.url).toBe('/list?sort=machine&dir=asc')
    const headers = [...page.root.querySelectorAll('[role=columnheader]')]
    expect(headers[2].getAttribute('aria-sort')).toBe('ascending')
    expect(headers[3].getAttribute('aria-sort')).toBe('none')
    expect(headers[2].querySelector('app-glyph')).not.toBeNull()
    expect(headers[3].querySelector('app-glyph')).toBeNull()
    expect(tasks(page.root).slice(0, 4)).toEqual(['fix-ci', 'add-search', 'x'.repeat(200), 'zeta'])
  })

  it('selects a row by click, opens the panel beside the list, and back removes the selection', async () => {
    const page = await open('/list')
    const history = vi.spyOn(page.router, 'navigate')
    await type(page, 'fix')
    await frame()
    await page.settle()
    ;(rowElements(page.root)[0].querySelectorAll('[role=gridcell]')[3] as HTMLElement).click()
    await page.settle()
    expect(page.router.url).toBe('/list?q=fix&sel=w1')
    // A selection is a history entry of its own, so back removes it.
    expect(history.mock.calls.at(-1)?.[1]).toMatchObject({ replaceUrl: false })
    const panel = page.root.querySelector('app-side-panel') as HTMLElement
    expect(text(panel.querySelector('.panel-body'))).toBe('Panel of fix-ci')
    expect(panel.querySelector('aside')?.getAttribute('aria-label')).toBe('Worktree fix-ci')
    // The list stays, with the selected row marked.
    expect(rowElements(page.root)[0].getAttribute('aria-selected')).toBe('true')
    expect(rowElements(page.root)[0].classList.contains('selected')).toBe(true)
    expect(page.root.querySelector('.layout')?.classList.contains('with-panel')).toBe(true)
    // What the back button does: the address of the entry before.
    await page.router.navigateByUrl('/list?q=fix')
    await page.settle()
    expect(page.router.url).toBe('/list?q=fix')
    expect(page.root.querySelector('app-side-panel')).toBeNull()
    expect(page.root.querySelector('.layout')?.classList.contains('with-panel')).toBe(false)
  })

  it('does not select a row for a click on a link or a button in it', async () => {
    const page = await open('/list')
    page.root.querySelector<HTMLElement>('.task')?.addEventListener('click', (event) => event.preventDefault())
    page.root.querySelector<HTMLElement>('.task')?.click()
    page.root.querySelector<HTMLElement>('.inner')?.click()
    // Nor for a click on the header or the empty space.
    page.root.querySelector<HTMLElement>('.head')?.click()
    page.viewport.click()
    await page.settle()
    expect(page.router.url).toBe('/list')
  })

  it('opens a pasted address in the same state, the panel included, without taking the focus', async () => {
    const page = await open('/list?q=zeta&chips=gone&machine=mach-alpha&sort=machine&dir=desc&sel=w5')
    expect(page.input.value).toBe('zeta')
    expect(tasks(page.root)).toEqual(['zeta'])
    expect(text(page.root.querySelector('.count'))).toBe('1 of 6')
    expect(button(page.root, 'Upstream gone').getAttribute('aria-pressed')).toBe('true')
    expect(button(page.root, 'alpha').getAttribute('aria-pressed')).toBe('true')
    expect(text(page.root.querySelector('.panel-body'))).toBe('Panel of zeta')
    expect(document.activeElement).not.toBe(page.root.querySelector('aside'))
    // The selected row is the one the keyboard starts on.
    expect(rowElements(page.root)[0].classList.contains('focused')).toBe(true)
  })

  it('ignores a bad sort, a chip the page does not list and a selection that names no entry', async () => {
    const page = await open('/list?sort=bogus&chips=nonsense&sel=nope')
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
    expect(text(page.root.querySelector('.count'))).toBe('6 of 6')
    expect(page.root.querySelector('app-side-panel')).toBeNull()
    expect([...page.root.querySelectorAll('[role=columnheader]')].map((header) => header.getAttribute('aria-sort'))).toEqual(['none', null, 'none', 'descending'])
  })

  it('catches the address up when it changes underneath, as the back button does', async () => {
    const page = await open('/list?q=zeta')
    await page.router.navigateByUrl('/list?q=fix')
    await page.settle()
    expect(page.input.value).toBe('fix')
    expect(tasks(page.root)).toEqual(['fix-ci'])
  })

  // cockpit-views#ac:rows-are-one-line-and-virtual, cockpit-views#ac:list-never-exceeds-60-row-elements
  it('renders only a window of the rows, whatever their number, and moves it as the list scrolls under a header that stays', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(864)
    const big = performanceFixture().document
    const page = await open('/list', big)
    const count = rowElements(page.root).length
    expect(big.worktrees).toHaveLength(600)
    expect(count).toBeGreaterThan(20)
    expect(count).toBeLessThanOrEqual(60)
    expect(page.viewport.getAttribute('aria-rowcount')).toBe('601')
    expect((page.root.querySelector('.body') as HTMLElement).style.height).toBe(`${600 * 32}px`)
    const first = rowElements(page.root)[0].getAttribute('aria-rowindex')
    expect(first).toBe('2')
    Object.defineProperty(page.viewport, 'scrollTop', { value: 6400, writable: true, configurable: true })
    page.viewport.dispatchEvent(new Event('scroll'))
    await page.settle()
    const scrolled = rowElements(page.root)
    expect(scrolled.length).toBeLessThanOrEqual(60)
    expect(Number(scrolled[0].getAttribute('aria-rowindex'))).toBeGreaterThan(150)
    expect((page.root.querySelector('.window') as HTMLElement).style.transform).toMatch(/translateY\(\d+px\)/)
    expect(page.root.querySelector('.head')).not.toBeNull()
    // A resize measures again.
    window.dispatchEvent(new Event('resize'))
    await page.settle()
    expect(rowElements(page.root).length).toBeLessThanOrEqual(60)
  })

  it('measures its own height, or the window\'s while it has none', async () => {
    const page = await open('/list')
    // jsdom has no layout: the height is the window's.
    expect(rowElements(page.root)).toHaveLength(6)
    expect(window.innerHeight).toBeGreaterThan(0)
  })

  // cockpit-views#ac:side-panel-opens-and-closes
  it('moves the focused row with j and k, opens the panel with Enter and closes it with Esc, returning to the list', async () => {
    const page = await open('/list')
    page.viewport.focus()
    const focused = () => rowElements(page.root).findIndex((row) => row.classList.contains('focused'))
    expect(focused()).toBe(0)
    expect(keydown(page.viewport, 'j').defaultPrevented).toBe(true)
    keydown(page.viewport, 'j')
    keydown(page.viewport, 'k')
    await page.settle()
    expect(focused()).toBe(1)
    expect(page.viewport.getAttribute('aria-activedescendant')).toBe(rowElements(page.root)[1].id)
    keydown(page.viewport, 'ArrowDown')
    keydown(page.viewport, 'ArrowUp')
    keydown(page.viewport, 'ArrowUp')
    keydown(page.viewport, 'ArrowUp')
    await page.settle()
    expect(focused()).toBe(0)
    keydown(page.viewport, 'j')
    keydown(page.viewport, 'j')
    keydown(page.viewport, 'k')
    expect(keydown(page.viewport, 'Enter').defaultPrevented).toBe(true)
    await page.settle()
    expect(page.router.url).toBe('/list?sel=w5')
    expect(text(page.root.querySelector('.panel-body'))).toBe('Panel of zeta')
    // The operator selected it, so the focus moved into the panel.
    expect(document.activeElement).toBe(page.root.querySelector('aside'))
    expect(closePanel?.()).toBe(true)
    await page.settle()
    expect(page.router.url).toBe('/list')
    expect(page.root.querySelector('app-side-panel')).toBeNull()
    expect(document.activeElement).toBe(page.viewport)
    expect(closePanel?.()).toBe(false)
  })

  it('stops at the first and last row, and leaves keys that are not its own alone', async () => {
    const page = await open('/list')
    page.viewport.focus()
    const focused = () => rowElements(page.root).findIndex((row) => row.classList.contains('focused'))
    keydown(page.viewport, 'k')
    for (let i = 0; i < 10; i++) keydown(page.viewport, 'j')
    await page.settle()
    expect(focused()).toBe(5)
    for (const init of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }, { isComposing: true }]) {
      expect(keydown(page.viewport, 'k', init).defaultPrevented).toBe(false)
    }
    expect(keydown(page.viewport, 'x').defaultPrevented).toBe(false)
    // A key typed in a link inside a row belongs to the link.
    keydown(page.root.querySelector('.task') as Element, 'k')
    await page.settle()
    expect(focused()).toBe(5)
    expect(page.router.url).toBe('/list')
  })

  it('scrolls the focused row into view', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(320)
    const page = await open('/list', performanceFixture().document)
    page.viewport.focus()
    for (let i = 0; i < 15; i++) keydown(page.viewport, 'j')
    await page.settle()
    expect(page.viewport.scrollTop).toBe(15 * 32 + 64 - 320)
    expect(page.viewport.getAttribute('aria-activedescendant')).not.toBeNull()
  })

  it('has no active row, and does nothing on Enter or j, while nothing is listed', async () => {
    const page = await open('/list', fleetDocument({ worktrees: [] }))
    page.viewport.focus()
    keydown(page.viewport, 'j')
    keydown(page.viewport, 'Enter')
    await page.settle()
    expect(page.router.url).toBe('/list')
    expect(page.viewport.hasAttribute('aria-activedescendant')).toBe(false)
  })

  // cockpit-views#ac:shortcuts-navigate-and-respect-typing
  it('registers its filter with the shell for / and Esc, and unregisters with the list', async () => {
    const page = await open('/list')
    const target = filterTarget as ListFilterTarget
    expect(target.element).toBe(page.input)
    target.focus()
    expect(document.activeElement).toBe(page.input)
    // An empty filter says so, and the shell lets Esc close the panel instead.
    expect(target.isEmpty()).toBe(true)
    await type(page, 'fix')
    expect(target.isEmpty()).toBe(false)
    target.clear()
    await page.settle()
    expect(page.input.value).toBe('')
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
    page.harness.fixture.destroy()
    expect(unregistered.filter).toHaveBeenCalled()
    expect(unregistered.panel).toHaveBeenCalled()
  })

  it('keeps what the operator does while the address is still catching up with what they typed', async () => {
    const page = await open('/list')
    page.input.value = 'fix'
    page.input.dispatchEvent(new Event('input', { bubbles: true }))
    // The frame writes the address; clear before it arrives.
    await frame()
    ;(filterTarget as ListFilterTarget).clear()
    page.input.value = 'fix'
    page.input.dispatchEvent(new Event('input', { bubbles: true }))
    ;(filterTarget as ListFilterTarget).clear()
    await frame()
    await page.settle()
    await frame()
    await page.settle()
    expect(page.input.value).toBe('')
    expect(page.router.url).toBe('/list')
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
  })

  it('does not take an address that arrives mid-navigation for news: what was typed stays until the navigation ends', async () => {
    const page = await open('/list')
    let release: () => void = () => undefined
    const held = new Promise<void>((done) => (release = done))
    const navigate = page.router.navigate.bind(page.router)
    vi.spyOn(page.router, 'navigate').mockImplementation(async (...args) => {
      await held
      return navigate(...args)
    })
    await type(page, 'fix')
    await frame()
    // The address changes underneath while the list's own navigation is still held.
    await page.router.navigateByUrl('/list?q=zeta')
    await page.settle()
    expect(page.input.value).toBe('fix')
    release()
    await frame()
    await page.settle()
    expect(page.router.url).toBe('/list?q=fix')
    expect(tasks(page.root)).toEqual(['fix-ci'])
  })

  it('clears the filter text with its own button, and explains the grammar on request', async () => {
    const page = await open('/list')
    expect(page.root.querySelector('button[aria-label="Clear the filter text"]')).toBeNull()
    await type(page, 'zeta')
    ;(page.root.querySelector('button[aria-label="Clear the filter text"]') as HTMLElement).click()
    await page.settle()
    expect(page.input.value).toBe('')
    expect(document.activeElement).toBe(page.input)
    const help = page.root.querySelector('button[aria-label="How the filter works"]') as HTMLElement
    expect(page.root.querySelector('.hint')).toBeNull()
    help.click()
    await page.settle()
    const hint = page.root.querySelector('.hint') as HTMLElement
    expect(help.getAttribute('aria-expanded')).toBe('true')
    expect(page.input.getAttribute('aria-describedby')).toBe(hint.id)
    expect(text(hint)).toContain('sneat-*/*-go')
    expect(text(hint)).toContain('branch, machine, repo, state, task')
    help.click()
    await page.settle()
    expect(page.root.querySelector('.hint')).toBeNull()
    expect(page.input.hasAttribute('aria-describedby')).toBe(false)
  })

  // cockpit-views#ac:empty-states-offer-clear
  it('names the filter that matched nothing and clears it with "Clear filters"', async () => {
    const page = await open('/list?q=zzz&chips=pr&machine=mach-beta&sort=machine&sel=w1')
    expect(tasks(page.root)).toEqual([])
    const empty = page.root.querySelector('.empty') as HTMLElement
    expect(text(empty)).toContain('No worktrees match the filter “zzz” and the Pull request filter and machine beta.')
    expect(text(page.root.querySelector('.count'))).toBe('0 of 6')
    button(page.root, 'Clear filters').click()
    await page.settle()
    expect(page.router.url).toBe('/list?sort=machine')
    expect(tasks(page.root)).toHaveLength(6)
    expect(page.input.value).toBe('')
  })

  it('says nothing has been observed yet when there is nothing at all', async () => {
    const page = await open('/list', fleetDocument({ worktrees: [] }))
    expect(text(page.root.querySelector('.empty'))).toBe('Nothing has been observed yet: no worktrees.')
    expect(button(page.root, 'Clear filters')).toBeUndefined()
    expect(text(page.root.querySelector('.count'))).toBe('0 of 0')
  })

  // cockpit-views#ac:warming-up-shows-progress
  it('shows fixed-height placeholder rows while nothing has loaded or the first scan has found nothing', async () => {
    const page = await open('/list')
    page.store.loaded.set(false)
    await page.settle()
    expect(page.root.querySelectorAll('.skeleton')).toHaveLength(SKELETON_ROWS)
    expect(page.root.querySelector('.viewport')?.getAttribute('aria-busy')).toBe('true')
    expect(rowElements(page.root)).toHaveLength(0)
    page.store.loaded.set(true)
    page.store.document.set(fleetDocument({ worktrees: [], warming_up: true }))
    await page.settle()
    expect(page.root.querySelectorAll('.skeleton')).toHaveLength(SKELETON_ROWS)
    page.store.document.set({ ...documentOf(), warming_up: true })
    await page.settle()
    expect(page.root.querySelectorAll('.skeleton')).toHaveLength(0)
    expect(rowElements(page.root)).toHaveLength(6)
    expect(page.root.querySelector('.viewport')?.getAttribute('aria-busy')).toBe('false')
  })

  // cockpit-views#ac:columns-are-few-and-uniform-ones-hidden
  it('hides a column that is the same default for every visible row, and shows it when one row differs', async () => {
    const same = documentOf()
    same.worktrees = same.worktrees.map((w) => ({ ...w, branch: w.task }))
    const hidden = await open('/list', same)
    expect([...hidden.root.querySelectorAll('[role=columnheader]')].map(text)).toEqual(['Worktree', 'Machine', 'Last activity'])
    same.worktrees[1] = { ...same.worktrees[1], branch: 'topic' }
    const shown = await open('/list', same)
    expect([...shown.root.querySelectorAll('[role=columnheader]')].map(text)).toEqual(['Worktree', 'Branch', 'Machine', 'Last activity'])
  })

  // cockpit-views#ac:no-recompute-when-unchanged
  it('does not filter and sort again for a poll that changes nothing, only for a new minute', async () => {
    const page = await open('/list')
    const before = page.list().result()
    page.store.now.set(NOW + 20_000)
    await page.settle()
    expect(page.list().result()).toBe(before)
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
    page.store.now.set(NOW + 2 * 60_000)
    await page.settle()
    expect(page.list().result()).not.toBe(before)
  })

  it('finds a selection with the page\'s own resolver, and opens no panel when the page has none', async () => {
    const page = await open('/list?sel=zeta', documentOf(), NoPanelHost)
    expect(page.root.querySelector('app-side-panel')).toBeNull()
    expect(page.root.querySelector('.layout')?.classList.contains('with-panel')).toBe(false)
    expect(rowElements(page.root).find((row) => row.classList.contains('selected'))?.textContent).toContain('zeta')
  })

  it('calls the panel Details when the page names it no better', async () => {
    const page = await open('/list?sel=w1', documentOf(), PlainHost)
    expect(page.root.querySelector('aside')?.getAttribute('aria-label')).toBe('Details')
  })

  it('renders on its own over an empty fleet', async () => {
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(ListView)
    fixture.componentRef.setInput('page', 'machines')
    fixture.componentRef.setInput('noun', 'machines')
    fixture.componentRef.setInput('rows', [])
    fixture.componentRef.setInput('columns', [])
    fixture.componentRef.setInput('chips', [])
    await fixture.whenStable()
    expect(text(fixture.nativeElement.querySelector('.count'))).toBe('0 of 0')
  })
})
