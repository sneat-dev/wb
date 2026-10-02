import { Component } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { FitChips } from './fit-chips'

@Component({
  imports: [FitChips],
  template: `<span class="chips" [appFitChips]="key">
    @for (name of names; track name) {
      <a class="machine"><span class="machine-name">{{ name }}</span></a>
    }
    @if (more > 0) {
      <span class="more" title="delta, echo">+{{ more }}</span>
    }
  </span>`,
})
class Host {
  names = ['alpha', 'beta', 'gamma']
  more = 0
  key = 0
}

/** Gives the chips a width and the row a room, as a layout would: jsdom has none. */
function layout(root: HTMLElement, room: number, chip = 60): void {
  const row = root.querySelector('.chips') as HTMLElement
  Object.defineProperty(row, 'clientWidth', { value: room, configurable: true })
  for (const element of root.querySelectorAll<HTMLElement>('.machine, .more')) Object.defineProperty(element, 'offsetWidth', { value: element.classList.contains('machine') ? chip : 30, configurable: true })
}

/** The cell's resize observers, which a test fires when it has given the cell its room. */
const observers: (() => void)[] = []

async function render(room: number | undefined, change: (host: Host) => void = () => undefined) {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(Host)
  change(fixture.componentInstance)
  fixture.autoDetectChanges()
  await fixture.whenStable()
  const root = fixture.nativeElement as HTMLElement
  if (room !== undefined) {
    root.querySelector('.chips')?.setAttribute('_ngcontent-a-c1', '')
    layout(root, room)
    observers.forEach((callback) => callback())
  }
  return { fixture, root }
}

const shown = (root: HTMLElement) => [...root.querySelectorAll('.machine:not([hidden])')].map((chip) => chip.textContent?.trim())
const note = (root: HTMLElement) => root.querySelector<HTMLElement>('.fitted')

describe('FitChips', () => {
  beforeEach(() => {
    observers.length = 0
    vi.stubGlobal('ResizeObserver', class { constructor(callback: () => void) { observers.push(callback) } observe = vi.fn(); disconnect = vi.fn() })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows every chip when they all fit, and measures nothing it cannot (no layout, one chip)', async () => {
    const wide = await render(400)
    expect(shown(wide.root)).toEqual(['alpha', 'beta', 'gamma'])
    expect(note(wide.root)).toBeNull()
    vi.unstubAllGlobals()
    const unmeasured = await render(undefined)
    expect(shown(unmeasured.root)).toEqual(['alpha', 'beta', 'gamma'])
    const single = await render(10, (host) => (host.names = ['alpha']))
    expect(shown(single.root)).toEqual(['alpha'])
  })

  it('shows as many whole chips as fit and a +n with the hidden names for the rest', async () => {
    const { root } = await render(160)
    expect(shown(root)).toEqual(['alpha', 'beta'])
    expect(note(root)?.textContent).toBe('+1')
    expect(note(root)?.title).toBe('gamma')
    expect(note(root)?.getAttribute('aria-label')).toBe('1 more: gamma')
    // It carries the scope attribute of the page's styles, so they apply to it.
    expect(note(root)?.hasAttribute('_ngcontent-a-c1')).toBe(true)
    // Measuring again changes nothing: its own note is not counted as the page's.
    observers.forEach((callback) => callback())
    expect(note(root)?.textContent).toBe('+1')
    const narrow = await render(80)
    expect(shown(narrow.root)).toEqual(['alpha'])
    expect(note(narrow.root)?.textContent).toBe('+2')
    expect(note(narrow.root)?.title).toBe('beta, gamma')
  })

  it("folds the page's own +n into its own, and shows the page's when nothing is hidden", async () => {
    const folded = await render(80, (host) => (host.more = 2))
    expect(note(folded.root)?.textContent).toBe('+4')
    expect(note(folded.root)?.title).toBe('beta, gamma, delta, echo')
    expect(folded.root.querySelector('.more:not(.fitted)')?.hasAttribute('hidden')).toBe(true)
    const fits = await render(400, (host) => (host.more = 2))
    expect(fits.root.querySelector('.more:not(.fitted)')?.hasAttribute('hidden')).toBe(false)
    expect(note(fits.root)).toBeNull()
  })

  it('shows them all again when the room grows', async () => {
    const { root } = await render(80)
    expect(note(root)?.hasAttribute('hidden')).toBe(false)
    layout(root, 400)
    observers.forEach((callback) => callback())
    expect(shown(root)).toEqual(['alpha', 'beta', 'gamma'])
    expect(note(root)?.hasAttribute('hidden')).toBe(true)
  })
})
