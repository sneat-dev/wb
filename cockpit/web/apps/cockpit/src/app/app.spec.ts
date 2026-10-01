import { Router, provideRouter } from '@angular/router'
import { TestBed } from '@angular/core/testing'
import { FleetStore } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { App, PAGE_LINKS } from './app'
import { appRoutes } from './app.routes'
import { providePageTitle } from './shell/page-title'
import { OverlayLoader } from './shell/overlay-loader'
import { ShellState } from './shell/shell-state'

describe('App', () => {
  let store: FleetStore
  let start: ReturnType<typeof vi.spyOn>
  let stop: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [provideRouter(appRoutes), providePageTitle()] })
    store = TestBed.inject(FleetStore)
    start = vi.spyOn(store, 'start').mockImplementation(() => undefined)
    stop = vi.spyOn(store, 'stop').mockImplementation(() => undefined)
    store.loaded.set(true)
    store.document.set(fleetDocument())
  })

  async function open(url: string) {
    const fixture = TestBed.createComponent(App)
    await TestBed.inject(Router).navigateByUrl(url)
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    return { fixture, root }
  }

  it('renders the top bar, the page and the overlays, with a tab for every page', async () => {
    const { root } = await open('/')
    expect([...root.querySelectorAll('nav a')].map((a) => a.textContent?.replace(/\d+|\s+| need.*|agents running|tasks need you/g, '').trim())).toEqual(PAGE_LINKS.map((link) => link.label))
    expect(root.querySelector('main router-outlet')).not.toBeNull()
    expect(root.querySelector('main app-fleet-banner')).not.toBeNull()
    // The palette and the sheet are a lazy chunk the shell fetches once it has rendered.
    await vi.waitFor(() => expect(root.querySelector('app-overlays app-command-palette')).not.toBeNull())
    expect(root.querySelector('app-overlays app-shortcut-sheet')).not.toBeNull()
  })

  it('opens the palette, held by the shell state, once the lazy overlays have arrived', async () => {
    const { fixture, root } = await open('/')
    TestBed.inject(ShellState).openPalette()
    await vi.waitFor(() => expect(root.querySelector('app-overlays [role="dialog"][aria-label="Search"]')).not.toBeNull())
    expect(fixture).toBeDefined()
  })

  // The sign-in card lives in the overlays chunk: a click on the chip before the idle prefetch must fetch it at once.
  it('fetches the overlays at once when the sign-in card is asked for, without waiting for the browser to be idle', async () => {
    const { fixture } = await open('/')
    const ensure = vi.spyOn(TestBed.inject(OverlayLoader), 'ensure')
    TestBed.inject(ShellState).toggleOwnerHint()
    await fixture.whenStable()
    expect(ensure).toHaveBeenCalled()
  })

  it('does not create the overlays in a shell that is already gone', async () => {
    const { fixture, root } = await open('/')
    fixture.destroy()
    // Nothing throws when the chunk arrives late.
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(root.querySelector('app-overlays')).toBeNull()
  })

  // cockpit-views#ac:home-route-and-alias
  it('shows Home at the root and at /dashboard, titled Home, with the Home tab current and no tab named Dashboard', async () => {
    for (const url of ['/', '/dashboard']) {
      TestBed.resetTestingModule()
      TestBed.configureTestingModule({ providers: [provideRouter(appRoutes), providePageTitle()] })
      const again = TestBed.inject(FleetStore)
      vi.spyOn(again, 'start').mockImplementation(() => undefined)
      again.loaded.set(true)
      again.document.set(fleetDocument())
      const { root } = await open(url)
      expect(document.title).toBe('Home')
      expect(TestBed.inject(Router).url).toBe('/')
      expect(root.querySelector('h1')?.textContent).toBe('Home')
      expect(root.querySelector('app-home-page')).not.toBeNull()
      expect(root.querySelector('nav a[aria-current="page"]')?.textContent).toContain('Home')
      expect(root.textContent).not.toContain('Dashboard')
    }
  })

  // cockpit-views#ac:no-heading-repeats-the-tab
  it('has one visually hidden h1 naming the page and the document, and no visible heading repeating the tab', async () => {
    const { root } = await open('/worktrees')
    const headings = [...root.querySelectorAll('h1, h2, h3')]
    expect(root.querySelectorAll('h1')).toHaveLength(1)
    const h1 = root.querySelector('h1') as HTMLElement
    expect(h1.textContent).toBe('Worktrees')
    expect(h1.classList.contains('visually-hidden')).toBe(true)
    expect(document.title).toBe('Worktrees')
    expect(headings.filter((heading) => heading !== h1 && heading.textContent?.trim() === 'Worktrees')).toEqual([])
    const current = root.querySelectorAll('nav a[aria-current="page"]')
    expect([...current].map((a) => a.textContent?.trim())).toEqual(['Worktrees'])
  })

  it('names the page after every navigation', async () => {
    const { fixture, root } = await open('/agents')
    expect(root.querySelector('h1')?.textContent).toBe('Agents')
    await TestBed.inject(Router).navigateByUrl('/tasks/detail?task=x')
    await fixture.whenStable()
    expect(root.querySelector('h1')?.textContent).toBe('Task')
  })

  it('starts the fleet store and the keyboard, and stops both when the shell goes', async () => {
    const { fixture } = await open('/')
    expect(start).toHaveBeenCalledTimes(1)
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: '?', bubbles: true }))
    expect(TestBed.inject(ShellState).sheetOpen()).toBe(true)
    TestBed.inject(ShellState).closeSheet()
    fixture.destroy()
    expect(stop).toHaveBeenCalledTimes(1)
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: '?', bubbles: true }))
    expect(TestBed.inject(ShellState).sheetOpen()).toBe(false)
  })

  // cockpit-views#ac:warming-up-shows-progress
  it('shows fixed-height skeleton rows below what is listed while the daemon warms up, and the scanned count in the chip', async () => {
    store.document.set(fleetDocument({ warming_up: true, repositories_scanned: 120, repositories_total: 438 }))
    const { fixture, root } = await open('/')
    expect(root.querySelectorAll('app-skeleton-rows.warming .skeleton-row')).toHaveLength(6)
    expect(root.querySelector('app-skeleton-rows.loading')).toBeNull()
    expect(root.querySelector('app-freshness-chip')?.textContent).toContain('scanned 120 of 438')
    expect(root.querySelector('main router-outlet')).not.toBeNull()
    store.document.set(fleetDocument())
    await fixture.whenStable()
    expect(root.querySelector('app-skeleton-rows')).toBeNull()
  })

  it('shows only skeleton rows, no page, before the first read is answered', async () => {
    store.loaded.set(false)
    const { root } = await open('/')
    expect(root.querySelector('main router-outlet')).toBeNull()
    expect(root.querySelector('app-skeleton-rows.loading')).not.toBeNull()
  })

  it('keeps the page hidden, behind skeleton rows, until a page has been routed to, and then swaps them without moving anything', async () => {
    const fixture = TestBed.createComponent(App)
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    // The first read is answered but no route has been activated yet.
    expect(root.querySelector('.page-slot')?.hasAttribute('hidden')).toBe(true)
    expect(root.querySelector('app-skeleton-rows.loading')).not.toBeNull()
    expect(root.querySelector('app-skeleton-rows.warming')).toBeNull()
    await TestBed.inject(Router).navigateByUrl('/')
    await fixture.whenStable()
    expect(root.querySelector('.page-slot')?.hasAttribute('hidden')).toBe(false)
    expect(root.querySelector('app-skeleton-rows')).toBeNull()
  })

  // cockpit-views#ac:client-accepts-only-schema-2, the shell half
  it('shows the schema-mismatch state and no page when the daemon is older, and reload when the page is', async () => {
    store.schemaMismatch.set('daemon-older')
    store.error.set('update wb on this machine')
    const { fixture, root } = await open('/')
    // The explanation is in the lazy overlays chunk, which is fetched at once for it.
    await vi.waitFor(() => expect(root.querySelector('app-schema-mismatch')?.textContent).toContain('update wb on this machine'))
    expect(root.querySelector('main router-outlet')).toBeNull()
    expect(root.querySelector('app-skeleton-rows')).toBeNull()
    expect(root.querySelectorAll('[role="alert"]')).toHaveLength(1)
    store.schemaMismatch.set('page-older')
    await fixture.whenStable()
    expect(root.querySelector('app-schema-mismatch')?.textContent).toContain('reload')
  })

  it('moves the focus to the page from the skip link', async () => {
    const { root } = await open('/')
    const link = root.querySelector('.skip-link') as HTMLAnchorElement
    const click = new MouseEvent('click', { bubbles: true, cancelable: true })
    link.dispatchEvent(click)
    expect(click.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(root.querySelector('main'))
  })
})
