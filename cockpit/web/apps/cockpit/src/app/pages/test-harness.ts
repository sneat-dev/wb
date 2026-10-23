import { Provider, Type, signal } from '@angular/core'
import { ComponentFixture, TestBed } from '@angular/core/testing'
import { provideRouter, withComponentInputBinding } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { vi } from 'vitest'
import { FETCH, FleetDocument, FleetStore, Session } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { UiClock } from '@cockpit/ui/control'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { appRoutes } from '../app.routes'
import { Shortcuts } from '../shortcuts/shortcuts'

/** Epoch milliseconds five minutes after the fixtures were observed. */
export const NOW = Date.parse('2026-10-01T10:05:00Z')

export const SESSION: Session = {
  principal: 'anonymous-local',
  capabilities: ['fleet.read'],
  code_browser_url: 'https://codegrapher.dev/',
}

/**
 * Opens `url` through the application's own routes, with the store holding
 * `document` and no network (a README read goes to `fetcher`, which a spec that
 * reads one must give), and returns the harness and the page component.
 */
export async function openPage<T>(url: string, page: Type<T>, document: FleetDocument = fleetDocument(), session: Session | null = SESSION, fetcher?: typeof fetch, extraProviders: Provider[] = []) {
  // A spec may open several pages; each gets its own injector.
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [
      provideRouter(appRoutes, withComponentInputBinding()),
      { provide: LIST_SHORTCUTS, useExisting: Shortcuts },
      { provide: UiClock, useValue: { now: signal(NOW) } },
      ...(fetcher ? [{ provide: FETCH, useValue: fetcher }] : []),
      ...extraProviders,
    ],
  })
  const store = TestBed.inject(FleetStore)
  store.document.set(document)
  store.session.set(session)
  store.sessionStatus.set(session ? 'ready' : 'failed')
  store.now.set(NOW)
  store.loaded.set(true)
  const harness = await RouterTestingHarness.create()
  const component = await harness.navigateByUrl(url, page)
  await harness.fixture.whenStable()
  return { harness, component, store, root: harness.routeNativeElement as HTMLElement }
}

/** Waits, through change detection, until `until` holds: for what a stubbed fetch answers after the page opened. */
export async function settle(fixture: ComponentFixture<unknown>, until: () => boolean): Promise<void> {
  await vi.waitFor(async () => {
    await fixture.whenStable()
    if (!until()) throw new Error('the page has not settled')
  })
}
