import { MediaMatcher } from '@angular/cdk/layout'
import { Component, computed, inject } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { By } from '@angular/platform-browser'
import { Router, provideRouter } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { FleetDocument, FleetStore, Worktree } from '@cockpit/fleet-data'
import { ListRow, buildWorktreeRows } from '@cockpit/fleet-data/list'
import { fleetDocument, performanceFixture, worktree } from '@cockpit/fleet-data/testing'
import { ClipboardWriter } from '../control/clipboard'
import { ListCell, ListPanelTemplate } from './list-cell'
import { LIST_SHORTCUTS, ListFilterTarget } from './list-host'
import { ListColumn } from './list-state'
import { SKELETON_ROWS, ListView } from './list-view'

const NOW = Date.parse('2026-10-01T10:05:00Z')
const DAY = 24 * 60 * 60 * 1000
const ago = (days: number) => new Date(NOW - days * DAY).toISOString()

const COLUMNS: ListColumn<Worktree>[] = [
  { id: 'worktree', header: 'Worktree', title: true, sort: 'worktree', width: 'fill', grow: 3, min: 100, value: (w) => w.task },
  { id: 'branch', header: 'Branch', width: 'fill', value: (w) => w.branch, empty: (w) => w.branch === w.task, priority: 1, hint: 'The branch' },
  { id: 'machine', header: 'Machine', sort: 'machine', width: 90, priority: 2, align: 'end', value: (w) => w.machine },
  { id: 'activity', header: 'Last activity', sort: 'activity', width: 100 },
]

/** How many times a row's own cell has been evaluated: a poll that changes nothing must not add to it. */
const counter = { renders: 0 }

@Component({
  imports: [ListView, ListCell, ListPanelTemplate],
  template: `<app-list page="worktrees" [columns]="columns" [panelLabel]="label" [prefix]="prefix">
    <ng-template appCell="worktree" let-w><a class="task" href="/somewhere">{{ tick(w) }}</a></ng-template>
    <ng-template appCell="machine" let-w><button type="button" class="inner">{{ w.machine }}</button></ng-template>
    <ng-template appListPanel let-w><p class="panel-body">Panel of {{ w.task }}</p><button type="button" class="panel-button">in panel</button></ng-template>
  </app-list>`,
})
class Host {
  protected readonly columns = COLUMNS
  protected readonly prefix = ''
  protected readonly label = (w: Worktree) => `Worktree ${w.task}`
  protected tick(w: Worktree): string {
    counter.renders++
    return w.task
  }
}

@Component({
  imports: [ListView, ListCell, ListPanelTemplate],
  template: `<app-list page="worktrees" [columns]="columns" prefix="a">
    <ng-template appCell="worktree" let-w><a class="task" href="/somewhere">{{ w.task }}</a></ng-template>
    <ng-template appListPanel let-w>{{ w.task }}</ng-template>
  </app-list>`,
})
class PrefixHost {
  protected readonly columns = COLUMNS
}

@Component({
  imports: [ListView, ListCell],
  template: `<app-list page="worktrees" [rows]="rows()" [columns]="columns" [resolve]="resolve">
    <ng-template appCell="worktree" let-w><a class="task" href="/somewhere">{{ w.task }}</a></ng-template>
  </app-list>`,
})
class NoPanelHost {
  protected readonly store = inject(FleetStore)
  protected readonly rows = computed(() => buildWorktreeRows(this.store.model()))
  protected readonly columns = COLUMNS
  protected readonly resolve = (rows: readonly ListRow<Worktree>[], sel: string) => rows.find((row) => row.item.task === sel)
}

@Component({
  imports: [ListView, ListCell, ListPanelTemplate],
  template: `<app-list page="worktrees" [columns]="columns">
    <ng-template appCell="worktree" let-w><a class="task" href="/somewhere">{{ w.task }}</a></ng-template>
    <ng-template appListPanel let-w>{{ w.task }}</ng-template>
  </app-list>`,
})
class PlainHost {
  protected readonly columns = COLUMNS
}

const FOOTER_COLUMNS: ListColumn<Worktree>[] = [...COLUMNS, { id: 'links', header: 'Links', width: 40, chrome: true }]

/** A page that tells the list what `c` copies, adds notes under the rows and has an actions cell. */
@Component({
  imports: [ListView, ListCell],
  template: `<app-list page="worktrees" [columns]="columns" [copyValue]="copyValue">
    <ng-template appCell="worktree" let-w><a class="task" href="/somewhere">{{ w.task }}</a></ng-template>
    <p listFooter class="page-note">showing the first 200</p>
  </app-list>`,
})
class FooterHost {
  protected readonly columns = FOOTER_COLUMNS
  protected readonly copyValue = (w: Worktree) => `id:${w.id}`
}

type AHost = typeof Host | typeof NoPanelHost | typeof PlainHost | typeof PrefixHost | typeof FooterHost

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

interface Options {
  document?: FleetDocument
  host?: AHost
  phone?: boolean
  copy?: (text: string) => Promise<boolean>
}

async function open(url: string, options: Options = {}) {
  const { document = documentOf(), host = Host, phone = false, copy = async () => true } = options
  TestBed.resetTestingModule()
  counter.renders = 0
  TestBed.configureTestingModule({
    providers: [
      provideRouter([{ path: 'list', component: host }]),
      { provide: ClipboardWriter, useValue: { copy } },
      { provide: MediaMatcher, useValue: { matchMedia: (media: string) => ({ matches: phone, media, addListener: () => undefined, removeListener: () => undefined }) } },
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
    list: () => harness.fixture.debugElement.query(By.directive(ListView)).componentInstance as { result: () => unknown; focused: () => number },
    viewport: root.querySelector('.viewport') as HTMLElement,
    input: root.querySelector('input') as HTMLInputElement,
    settle: () => harness.fixture.whenStable(),
  }
}

type Page = Awaited<ReturnType<typeof open>>

const rowElements = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role=row][aria-rowindex]')].filter((row) => row.dataset['index'] !== undefined)
const tasks = (root: HTMLElement) => rowElements(root).map((row) => row.querySelector('.task')?.textContent)
const frame = () => new Promise((done) => setTimeout(done, 40))
const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const button = (root: HTMLElement, name: string) => [...root.querySelectorAll('button')].find((candidate) => text(candidate) === name) as HTMLButtonElement
const headers = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('.head [role=columnheader]:not(.open-cell)')]
const focusedTask = (root: HTMLElement) => text(root.querySelector('.row.focused .task'))

function keydown(target: Element, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init })
  target.dispatchEvent(event)
  return event
}

async function type(page: Page, value: string) {
  page.input.value = value
  page.input.dispatchEvent(new Event('input', { bubbles: true }))
  await page.settle()
}

class FakeObserver {
  static last: FakeObserver
  disconnect = vi.fn()
  observe = vi.fn()
  constructor(readonly callback: () => void) {
    FakeObserver.last = this
  }
}

describe('ListView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('lists the rows in the default order under a header, with the count, and one line per row with the full value in its title', async () => {
    const { root } = await open('/list')
    expect(tasks(root)).toEqual(DEFAULT_ORDER)
    expect(text(root.querySelector('.count'))).toBe('6 of 6')
    expect(headers(root).map(text)).toEqual(['Worktree', 'Branch', 'Machine', 'Last activity'])
    const long = rowElements(root)[3].querySelector('[role=gridcell]') as HTMLElement
    expect(long.getAttribute('title')).toBe('x'.repeat(200))
    // A column with no value has no title, and a cell with no template shows its value.
    expect(rowElements(root)[0].querySelectorAll('[role=gridcell]')[3].hasAttribute('title')).toBe(false)
    expect(text(rowElements(root)[0].querySelectorAll('[role=gridcell]')[1])).toBe('topic')
    expect(root.querySelector('.viewport')?.getAttribute('aria-rowcount')).toBe('7')
    expect(root.querySelector('.viewport')?.getAttribute('aria-colcount')).toBe('5')
    expect(root.querySelector('.head')?.getAttribute('aria-rowindex')).toBe('1')
    expect(rowElements(root)[0].getAttribute('aria-rowindex')).toBe('2')
    expect(root.querySelector('app-side-panel')).toBeNull()
    expect((root.querySelector('app-list') as HTMLElement).style.getPropertyValue('--row-h')).toBe('32px')
  })

  it('marks how a header reads and sorts', async () => {
    const page = await open('/list')
    const columns = headers(page.root)
    expect(columns.map((header) => header.getAttribute('aria-sort'))).toEqual(['none', null, 'none', 'descending'])
    expect(columns[1].getAttribute('title')).toBe('The branch')
    expect(columns[2].classList.contains('end')).toBe(true)
    // A `title` header is the page's visible title; the others are not.
    expect(columns.map((header) => header.classList.contains('title'))).toEqual([true, false, false, false])
    // The tracks: a fill column is minmax(min, grow fr), a fixed one takes up to its width, and the open-page cell is reserved.
    const grid = (page.root.querySelector('.viewport') as HTMLElement).style.getPropertyValue('--cols')
    expect(grid).toBe('minmax(100px, 3fr) minmax(0px, 1fr) minmax(90px, 90px) minmax(100px, 100px) 32px')
  })

  it('takes the noun, the chips and their words from the page alone', async () => {
    const { root } = await open('/list')
    expect(root.querySelector('.viewport')?.getAttribute('aria-label')).toBe('worktrees')
    const chips = [...root.querySelectorAll('[aria-label="Quick filters"] button')].map(text)
    expect(chips).toEqual(['Active', 'Orphaned', 'Unpushed', 'Upstream gone', 'Has pull request', 'Idle 30 days', 'Safe to remove', 'Needs a look'])
    expect(button(root, 'Unpushed').getAttribute('title')).toContain('not pushed')
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

  it('writes a toggled chip at once and drops the queued write of the text, which the chip carries', async () => {
    const page = await open('/list')
    const history = vi.spyOn(page.router, 'navigate')
    await type(page, 'fix')
    button(page.root, 'Active').click()
    await frame()
    await page.settle()
    expect(history).toHaveBeenCalledTimes(1)
    expect(page.router.url).toBe('/list?q=fix&chips=active')
  })

  it('toggles a quick filter and a machine, each in the address, and the rows are the ones that satisfy them', async () => {
    const page = await open('/list')
    button(page.root, 'Unpushed').click()
    await page.settle()
    expect(page.router.url).toBe('/list?chips=unpushed')
    expect(tasks(page.root)).toEqual(['fix-ci'])
    expect(button(page.root, 'Unpushed').getAttribute('aria-pressed')).toBe('true')
    button(page.root, 'beta').click()
    await page.settle()
    expect(page.router.url).toBe('/list?chips=unpushed&machine=mach-beta')
    expect(tasks(page.root)).toEqual([])
    button(page.root, 'Unpushed').click()
    await page.settle()
    expect(tasks(page.root)).toEqual(['far-task', 'beta-task'])
    button(page.root, 'beta').click()
    await page.settle()
    expect(page.router.url).toBe('/list')
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
    button(page.root, 'Upstream gone').click()
    await page.settle()
    expect(tasks(page.root)).toEqual(['zeta'])
  })

  it('offers the machine chips only when there is more than one machine, or one is selected', async () => {
    const single = { ...documentOf(), machines: [documentOf().machines[0]] }
    const one = await open('/list', { document: single })
    expect(one.root.querySelector('[aria-label=Machines]')).toBeNull()
    const selected = await open('/list?machine=mach-alpha', { document: single })
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
    const columns = headers(page.root)
    expect(columns[2].getAttribute('aria-sort')).toBe('ascending')
    expect(columns[3].getAttribute('aria-sort')).toBe('none')
    expect(columns[2].querySelector('app-glyph')).not.toBeNull()
    expect(columns[3].querySelector('app-glyph')).toBeNull()
    expect(tasks(page.root).slice(0, 4)).toEqual(['fix-ci', 'add-search', 'x'.repeat(200), 'zeta'])
  })

  // cockpit-views#ac:filter-state-lives-in-the-address (back and forward)
  it('restores each earlier state when the address goes back: the filter, a chip, a sort and a selection', async () => {
    const page = await open('/list')
    const states = ['/list?q=fix', '/list?q=fix&chips=unpushed', '/list?q=fix&chips=unpushed&sort=machine&dir=asc', '/list?q=fix&chips=unpushed&sort=machine&dir=asc&sel=w1']
    await type(page, 'fix')
    await frame()
    await page.settle()
    button(page.root, 'Unpushed').click()
    await page.settle()
    button(page.root, 'Machine').click()
    await page.settle()
    ;(rowElements(page.root)[0].querySelectorAll('[role=gridcell]')[3] as HTMLElement).click()
    await page.settle()
    expect(page.router.url).toBe(states[3])
    // What the back button does at each step: the address of the entry before.
    for (const url of [states[2], states[1], states[0], '/list']) {
      await page.router.navigateByUrl(url)
      await page.settle()
      expect(page.input.value).toBe(url === '/list' ? '' : 'fix')
      expect(button(page.root, 'Unpushed').getAttribute('aria-pressed')).toBe(String(url.includes('chips')))
      expect(headers(page.root)[2].getAttribute('aria-sort')).toBe(url.includes('sort=machine') ? 'ascending' : 'none')
      expect(page.root.querySelector('app-side-panel')).toBeNull()
    }
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
    await page.router.navigateByUrl(states[3])
    await page.settle()
    expect(tasks(page.root)).toEqual(['fix-ci'])
    expect(text(page.root.querySelector('.panel-body'))).toBe('Panel of fix-ci')
  })

  it('selects a row by click and opens the panel beside the list as a history entry, then moves between rows without adding one', async () => {
    const page = await open('/list')
    const history = vi.spyOn(page.router, 'navigate')
    ;(rowElements(page.root)[0].querySelectorAll('[role=gridcell]')[3] as HTMLElement).click()
    await page.settle()
    expect(page.router.url).toBe('/list?sel=w2')
    expect(history.mock.calls.at(-1)?.[1]).toMatchObject({ replaceUrl: false })
    const panel = page.root.querySelector('app-side-panel') as HTMLElement
    expect(text(panel.querySelector('.panel-body'))).toBe('Panel of add-search')
    expect(panel.querySelector('.side-panel')?.getAttribute('aria-label')).toBe('Worktree add-search')
    expect(rowElements(page.root)[0].getAttribute('aria-selected')).toBe('true')
    expect(rowElements(page.root)[0].classList.contains('selected')).toBe(true)
    expect(page.root.querySelector('.layout')?.classList.contains('with-panel')).toBe(true)
    // Another row replaces the entry; closing is an entry of its own.
    ;(rowElements(page.root)[1].querySelectorAll('[role=gridcell]')[3] as HTMLElement).click()
    await page.settle()
    expect(page.router.url).toBe('/list?sel=w5')
    expect(history.mock.calls.at(-1)?.[1]).toMatchObject({ replaceUrl: true })
    expect(closePanel?.()).toBe(true)
    await page.settle()
    expect(history.mock.calls.at(-1)?.[1]).toMatchObject({ replaceUrl: false })
    expect(page.router.url).toBe('/list')
  })

  it('does not select a row for a click on a link or a button in it, on the header, or on the empty space', async () => {
    const page = await open('/list')
    page.root.querySelector<HTMLElement>('.task')?.addEventListener('click', (event) => event.preventDefault())
    page.root.querySelector<HTMLElement>('.task')?.click()
    page.root.querySelector<HTMLElement>('.inner')?.click()
    page.root.querySelector<HTMLElement>('.head')?.click()
    page.viewport.click()
    await page.settle()
    expect(page.router.url).toBe('/list')
  })

  it('has one tab stop: the list, with the links and buttons of its rows out of the tab order', async () => {
    const page = await open('/list')
    const controls = [...page.root.querySelectorAll<HTMLElement>('.row a, .row button')]
    expect(controls.length).toBeGreaterThan(10)
    expect(controls.every((control) => control.getAttribute('tabindex') === '-1')).toBe(true)
    expect(page.viewport.getAttribute('tabindex')).toBe('0')
  })

  it('offers each row\'s own page as a button at the row end, out of the tab order', async () => {
    const page = await open('/list')
    const open_ = rowElements(page.root)[0].querySelector('a.open') as HTMLAnchorElement
    expect(open_.getAttribute('href')).toBe('/worktrees/w2')
    expect(open_.getAttribute('aria-label')).toBe('Open add-search')
    expect(open_.getAttribute('tabindex')).toBe('-1')
    expect(page.root.querySelector('.head .open-cell')?.getAttribute('aria-label')).toBe('Open page')
  })

  it('opens a pasted address in the same state, the panel included, without taking the focus, with the selected row in view', async () => {
    const page = await open('/list?q=zeta&chips=gone&machine=mach-alpha&sort=machine&dir=desc&sel=w5')
    expect(page.input.value).toBe('zeta')
    expect(tasks(page.root)).toEqual(['zeta'])
    expect(text(page.root.querySelector('.count'))).toBe('1 of 6')
    expect(button(page.root, 'Upstream gone').getAttribute('aria-pressed')).toBe('true')
    expect(button(page.root, 'alpha').getAttribute('aria-pressed')).toBe('true')
    expect(text(page.root.querySelector('.panel-body'))).toBe('Panel of zeta')
    expect(document.activeElement).not.toBe(page.root.querySelector('.side-panel'))
    expect(rowElements(page.root)[0].classList.contains('focused')).toBe(true)
  })

  it('ignores a bad sort, a chip the page does not list, a machine the fleet does not have and a selection that names no entry', async () => {
    const page = await open('/list?sort=bogus&chips=nonsense&sel=nope&machine=mach-nope')
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
    expect(text(page.root.querySelector('.count'))).toBe('6 of 6')
    expect(page.root.querySelector('app-side-panel')).toBeNull()
    expect(headers(page.root).map((header) => header.getAttribute('aria-sort'))).toEqual(['none', null, 'none', 'descending'])
    expect(button(page.root, 'alpha').getAttribute('aria-pressed')).toBe('false')
    const known = await open('/list?machine=mach-beta')
    expect(tasks(known.root)).toEqual(['far-task', 'beta-task'])
  })

  it('keeps a machine in the address while the fleet is not yet known', async () => {
    const page = await open('/list?machine=mach-beta', { document: fleetDocument({ machines: [], worktrees: [] }) })
    expect(page.root.querySelector('[aria-label=Machines]')).toBeNull()
    expect(page.router.url).toBe('/list?machine=mach-beta')
  })

  it('catches the address up when it changes underneath, as the back button does', async () => {
    const page = await open('/list?q=zeta')
    await page.router.navigateByUrl('/list?q=fix')
    await page.settle()
    expect(page.input.value).toBe('fix')
    expect(tasks(page.root)).toEqual(['fix-ci'])
  })

  it('does not take an address that carries what the list itself is writing for news, but takes any other mid-navigation', async () => {
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
    // The address changes underneath to the text being written: no news.
    await page.router.navigateByUrl('/list?q=fix')
    await page.settle()
    expect(page.input.value).toBe('fix')
    // Another address (back to an earlier entry) while the write is held is news.
    await page.router.navigateByUrl('/list?q=zeta')
    await page.settle()
    expect(page.input.value).toBe('zeta')
    release()
    await frame()
    await page.settle()
  })

  it('keeps two lists on one page apart with a prefix', async () => {
    const page = await open('/list?q=zeta&a.q=fix', { host: PrefixHost })
    expect(page.input.value).toBe('fix')
    expect(tasks(page.root)).toEqual(['fix-ci'])
    await type(page, 'fix-')
    await frame()
    await page.settle()
    expect(page.router.url).toBe('/list?q=zeta&a.q=fix-')
    ;(rowElements(page.root)[0].querySelectorAll('[role=gridcell]')[3] as HTMLElement).click()
    await page.settle()
    expect(page.router.url).toContain('a.sel=')
  })

  // cockpit-views#ac:rows-are-one-line-and-virtual, cockpit-views#ac:list-never-exceeds-60-row-elements
  it('renders only a window of the rows, whatever their number, and moves it as the list scrolls to the very end', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(864)
    const big = performanceFixture().document
    const page = await open('/list', { document: big })
    const count = rowElements(page.root).length
    expect(big.worktrees).toHaveLength(600)
    expect(count).toBeGreaterThan(20)
    expect(count).toBeLessThanOrEqual(80)
    expect(page.viewport.getAttribute('aria-rowcount')).toBe('601')
    expect((page.root.querySelector('.body') as HTMLElement).style.height).toBe(`${600 * 32}px`)
    expect(rowElements(page.root)[0].getAttribute('aria-rowindex')).toBe('2')
    Object.defineProperty(page.viewport, 'scrollTop', { value: 600 * 32, writable: true, configurable: true })
    page.viewport.dispatchEvent(new Event('scroll'))
    await page.settle()
    const end = rowElements(page.root)
    expect(end.length).toBeLessThanOrEqual(80)
    expect(end.at(-1)?.getAttribute('aria-rowindex')).toBe('601')
    expect(page.root.querySelector('.head')).not.toBeNull()
  })

  it('measures again when the viewport changes size, as when data replaces the placeholders, and stops watching with the list', async () => {
    vi.stubGlobal('ResizeObserver', FakeObserver)
    const height = vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(100)
    const big = performanceFixture().document
    const page = await open('/list', { document: big })
    const before = rowElements(page.root).length
    expect(FakeObserver.last.observe).toHaveBeenCalledWith(page.viewport)
    // The viewport grew once the data arrived: the observer says so, and the window follows with no scroll.
    height.mockReturnValue(1000)
    FakeObserver.last.callback()
    await page.settle()
    expect(rowElements(page.root).length).toBeGreaterThan(before)
    const last = rowElements(page.root).at(-1) as HTMLElement
    expect(Number(last.dataset['index']) * 32 + 32).toBeGreaterThanOrEqual(1000 - 32)
    page.harness.fixture.destroy()
    expect(FakeObserver.last.disconnect).toHaveBeenCalled()
  })

  it('hides the lowest-priority columns, not squeezes them all, when the list is narrower than its columns need, and brings them back when it widens', async () => {
    vi.stubGlobal('ResizeObserver', FakeObserver)
    const width = vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1000)
    const page = await open('/list')
    expect(headers(page.root).map(text)).toEqual(['Worktree', 'Branch', 'Machine', 'Last activity'])
    width.mockReturnValue(300)
    FakeObserver.last.callback()
    await page.settle()
    expect(headers(page.root).map(text)).toEqual(['Worktree', 'Last activity'])
    expect((page.root.querySelector('.viewport') as HTMLElement).getAttribute('aria-colcount')).toBe('3')
    expect(rowElements(page.root)[0].querySelectorAll('[role=gridcell]')).toHaveLength(3)
    width.mockReturnValue(1000)
    FakeObserver.last.callback()
    await page.settle()
    expect(headers(page.root)).toHaveLength(4)
  })

  it('measures its own height, or the window\'s while it has none', async () => {
    const page = await open('/list')
    expect(rowElements(page.root)).toHaveLength(6)
    expect(window.innerHeight).toBeGreaterThan(0)
  })

  // cockpit-views#ac:side-panel-opens-and-closes
  it('moves the focused row with j and k, opens the panel with Enter, and Esc closes it returning to the row that was open', async () => {
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
    expect(keydown(page.viewport, 'Enter').defaultPrevented).toBe(true)
    await page.settle()
    expect(page.router.url).toBe('/list?sel=w5')
    expect(text(page.root.querySelector('.panel-body'))).toBe('Panel of zeta')
    // Beside the list the focus stays in the list; the panel is for Tab or a second Enter.
    expect(document.activeElement).toBe(page.viewport)
    expect(closePanel?.()).toBe(true)
    await page.settle()
    expect(page.router.url).toBe('/list')
    expect(page.root.querySelector('app-side-panel')).toBeNull()
    expect(document.activeElement).toBe(page.viewport)
    expect(focused()).toBe(1)
    expect(closePanel?.()).toBe(false)
  })

  it('keeps browsing with j and k while the panel is open: the panel follows, the focus stays, Enter moves into the panel', async () => {
    const page = await open('/list?sel=w2')
    page.viewport.focus()
    const history = vi.spyOn(page.router, 'navigate')
    keydown(page.viewport, 'j')
    await page.settle()
    expect(page.router.url).toBe('/list?sel=w5')
    expect(text(page.root.querySelector('.panel-body'))).toBe('Panel of zeta')
    expect(history.mock.calls.at(-1)?.[1]).toMatchObject({ replaceUrl: true })
    expect(document.activeElement).toBe(page.viewport)
    keydown(page.viewport, 'Enter')
    await page.settle()
    expect(document.activeElement).toBe(page.root.querySelector('.side-panel'))
    // Tab reaches the panel next in the document order: its controls follow the list.
    expect(page.root.querySelector('.panel-button')).not.toBeNull()
  })

  it('keeps the keyboard on the same row across polls and minutes, falls back to the nearest row when it goes, and holds it across Esc', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(864)
    const big = performanceFixture().document
    const page = await open('/list', { document: big })
    page.viewport.focus()
    for (let i = 0; i < 300; i++) keydown(page.viewport, 'j')
    await page.settle()
    expect(page.list().focused()).toBe(300)
    const name = focusedTask(page.root)
    expect(name).not.toBe('')
    // A poll with a new document, and a new minute: still on that row, not the first.
    page.store.document.set({ ...big })
    page.store.now.set(NOW + 5 * 60_000)
    await page.settle()
    expect(page.list().focused()).toBe(300)
    expect(focusedTask(page.root)).toBe(name)
    // Open it, move on, close: the keyboard is on the row that was last focused.
    keydown(page.viewport, 'Enter')
    await page.settle()
    expect(closePanel?.()).toBe(true)
    await page.settle()
    expect(page.list().focused()).toBe(300)
    // The row goes: the keyboard lands near where it was.
    const gone = page.root.querySelector('.row.focused .task')?.textContent
    page.store.document.set({ ...big, worktrees: big.worktrees.filter((w) => w.task !== gone) })
    await page.settle()
    expect(page.list().focused()).toBe(300)
    expect(focusedTask(page.root)).not.toBe(gone)
    page.store.document.set({ ...big, worktrees: big.worktrees.slice(0, 10) })
    await page.settle()
    expect(page.list().focused()).toBe(9)
  })

  it('moves a page at a time with PageUp and PageDown, to the ends with Home and End', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(320)
    const page = await open('/list', { document: performanceFixture().document })
    page.viewport.focus()
    keydown(page.viewport, 'PageDown')
    expect(page.list().focused()).toBe(9)
    keydown(page.viewport, 'PageUp')
    expect(page.list().focused()).toBe(0)
    keydown(page.viewport, 'End')
    expect(page.list().focused()).toBe(599)
    keydown(page.viewport, 'Home')
    expect(page.list().focused()).toBe(0)
  })

  it('opens the focused row\'s page with o, and copies its name with c, saying so through the one live region', async () => {
    const copy = vi.fn(async () => true)
    const page = await open('/list', { copy })
    page.viewport.focus()
    const navigate = vi.spyOn(page.router, 'navigateByUrl')
    keydown(page.viewport, 'o')
    expect(navigate).toHaveBeenCalledWith('/worktrees/w2')
    keydown(page.viewport, 'c')
    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith('add-search'))
    await vi.waitFor(() => expect(text(page.root.querySelector('section > [role=status]'))).toBe('Copied add-search'))
    copy.mockResolvedValue(false)
    keydown(page.viewport, 'c')
    await vi.waitFor(() => expect(text(page.root.querySelector('section > [role=status]'))).toBe('Copy failed'))
    expect(page.root.querySelectorAll('.row [role=status]')).toHaveLength(0)
  })

  // The page may say what c copies, and may put notes under the rows (the Agents page).
  it('copies what the page says with c, shows the page\'s notes under the rows, and draws an actions cell\'s header for assistive technology only', async () => {
    const copy = vi.fn(async () => true)
    const page = await open('/list', { copy, host: FooterHost })
    page.viewport.focus()
    keydown(page.viewport, 'c')
    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith('id:w2'))
    await vi.waitFor(() => expect(text(page.root.querySelector('section > [role=status]'))).toBe('Copied id:w2'))
    expect(text(page.root.querySelector('.footer .page-note'))).toBe('showing the first 200')
    const links = headers(page.root).find((header) => text(header) === 'Links') as HTMLElement
    expect(links.querySelector('.visually-hidden')?.textContent).toBe('Links')
    expect(links.querySelector('button')).toBeNull()
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
    keydown(page.root.querySelector('.task') as Element, 'k')
    await page.settle()
    expect(focused()).toBe(5)
    expect(page.router.url).toBe('/list')
  })

  it('scrolls the focused row into view', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(320)
    const page = await open('/list', { document: performanceFixture().document })
    page.viewport.focus()
    for (let i = 0; i < 15; i++) keydown(page.viewport, 'j')
    await page.settle()
    expect(page.viewport.scrollTop).toBe(15 * 32 + 64 - 320)
    expect(page.viewport.getAttribute('aria-activedescendant')).not.toBeNull()
  })

  it('scrolls a selection of the address into view, as a deep link and back from a page do', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(320)
    const big = performanceFixture().document
    const page = await open('/list', { document: big })
    const id = (page.list().result() as { rows: { id: string }[] }).rows[400].id
    const deep = await open(`/list?sel=${id}`, { document: big })
    expect(deep.viewport.scrollTop).toBeGreaterThan(400 * 32 - 320)
    expect(deep.root.querySelector('.row.selected')).not.toBeNull()
  })

  it('has no active row, and does nothing on Enter, o, c or j, while nothing is listed', async () => {
    const page = await open('/list', { document: fleetDocument({ worktrees: [] }) })
    page.viewport.focus()
    for (const key of ['j', 'Enter', 'o', 'c']) keydown(page.viewport, key)
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

  // cockpit-views#ac:empty-states-offer-clear
  it('names the filter that matched nothing and clears it with "Clear filters"', async () => {
    const page = await open('/list?q=zzz&chips=pr&machine=mach-beta&sort=machine&sel=w1')
    expect(tasks(page.root)).toEqual([])
    expect(text(page.root.querySelector('.empty'))).toContain('No worktrees match the filter “zzz” and the Has pull request filter and machine beta.')
    expect(text(page.root.querySelector('.count'))).toBe('0 of 6')
    button(page.root, 'Clear filters').click()
    await page.settle()
    expect(page.router.url).toBe('/list?sort=machine')
    expect(tasks(page.root)).toHaveLength(6)
    expect(page.input.value).toBe('')
  })

  it('says nothing has been observed yet when there is nothing at all', async () => {
    const page = await open('/list', { document: fleetDocument({ worktrees: [] }) })
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
    const hidden = await open('/list', { document: same })
    expect(headers(hidden.root).map(text)).toEqual(['Worktree', 'Machine', 'Last activity'])
    same.worktrees[1] = { ...same.worktrees[1], branch: 'topic' }
    const shown = await open('/list', { document: same })
    expect(headers(shown.root).map(text)).toEqual(['Worktree', 'Branch', 'Machine', 'Last activity'])
  })

  // cockpit-views#ac:no-recompute-when-unchanged
  it('renders no row again for a poll that changes nothing, or a new clock reading within the minute', async () => {
    const page = await open('/list')
    // Development mode checks each binding twice; let that settle before counting.
    page.harness.detectChanges()
    const before = counter.renders
    expect(before).toBeGreaterThanOrEqual(6)
    page.store.document.set(page.store.document())
    page.store.now.set(NOW + 20_000)
    await page.settle()
    page.store.now.set(NOW + 50_000)
    await page.settle()
    expect(counter.renders).toBe(before)
    expect(tasks(page.root)).toEqual(DEFAULT_ORDER)
  })

  it('reads the clock only for a filter that asks for ages', async () => {
    const page = await open('/list?q=age:<1d')
    expect(tasks(page.root)).toEqual([])
    // Two days later the 1 d old row is older than a day for good, and ten minutes later nothing else changes: the bucket moves, the rows do not.
    const result = page.list().result()
    page.store.now.set(NOW + 5 * 60_000)
    await page.settle()
    expect(page.list().result()).not.toBe(result)
    const chip = await open('/list?chips=idle30')
    expect(tasks(chip.root)).toEqual(['far-task'])
  })

  it('shows "no longer in the fleet" in the panel of a selection that was there and has gone, and offers to close it', async () => {
    const page = await open('/list?sel=w2')
    expect(text(page.root.querySelector('.panel-body'))).toBe('Panel of add-search')
    page.store.document.set({ ...documentOf(), worktrees: documentOf().worktrees.filter((w) => w.id !== 'w2') })
    await page.settle()
    expect(text(page.root.querySelector('app-side-panel .gone'))).toBe('This is no longer in the fleet.')
    expect(page.router.url).toBe('/list?sel=w2')
    expect(closePanel?.()).toBe(true)
    await page.settle()
    expect(page.router.url).toBe('/list')
    expect(page.root.querySelector('app-side-panel')).toBeNull()
  })

  it('is a modal sheet on a phone: focus goes in even for a pasted address, the list is inert, and Esc closes it', async () => {
    const page = await open('/list?sel=w2', { phone: true })
    const aside = page.root.querySelector('.side-panel') as HTMLElement
    expect(aside.getAttribute('role')).toBe('dialog')
    expect(document.activeElement).toBe(aside)
    expect(page.root.querySelector('section.list')?.hasAttribute('inert')).toBe(true)
    expect(closePanel?.()).toBe(true)
    await page.settle()
    expect(page.root.querySelector('section.list')?.hasAttribute('inert')).toBe(false)
    const beside = await open('/list?sel=w2')
    expect(beside.root.querySelector('section.list')?.hasAttribute('inert')).toBe(false)
  })

  it('finds a selection with the page\'s own resolver, and opens no panel when the page has none', async () => {
    const page = await open('/list?sel=zeta', { host: NoPanelHost })
    expect(page.root.querySelector('app-side-panel')).toBeNull()
    expect(page.root.querySelector('.layout')?.classList.contains('with-panel')).toBe(false)
    expect(rowElements(page.root).find((row) => row.classList.contains('selected'))?.textContent).toContain('zeta')
  })

  it('calls the panel Details when the page names it no better', async () => {
    const page = await open('/list?sel=w1', { host: PlainHost })
    expect(page.root.querySelector('.side-panel')?.getAttribute('aria-label')).toBe('Details')
  })

  it('renders on its own over an empty fleet, with the rows of its page', async () => {
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: LIST_SHORTCUTS, useValue: { registerFilter: () => () => undefined, registerPanel: () => () => undefined } }] })
    const fixture = TestBed.createComponent(ListView)
    fixture.componentRef.setInput('page', 'machines')
    fixture.componentRef.setInput('columns', [])
    fixture.componentRef.setInput('extraChips', [{ id: 'runtime-x', label: 'X' }])
    await fixture.whenStable()
    expect(text(fixture.nativeElement.querySelector('.count'))).toBe('0 of 0')
    expect(text(fixture.nativeElement.querySelector('[aria-label="Quick filters"]'))).toBe('Stale Older WB X')
  })
})
