import { Type } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { provideRouter, withComponentInputBinding } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { FleetDocument, FleetStore, Session } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { appRoutes } from '../app.routes'

/** Epoch milliseconds five minutes after the fixtures were observed. */
export const NOW = Date.parse('2026-10-01T10:05:00Z')

export const SESSION: Session = {
  principal: 'anonymous-local',
  capabilities: ['fleet.read'],
  code_browser_url: 'https://codegrapher.dev/',
}

/**
 * Opens `url` through the application's own routes, with the store holding
 * `document` and no network, and returns the harness and the page component.
 */
export async function openPage<T>(url: string, page: Type<T>, document: FleetDocument = fleetDocument(), session: Session | null = SESSION) {
  // A spec may open several pages; each gets its own injector.
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter(appRoutes, withComponentInputBinding())] })
  const store = TestBed.inject(FleetStore)
  store.document.set(document)
  store.session.set(session)
  store.now.set(NOW)
  const harness = await RouterTestingHarness.create()
  const component = await harness.navigateByUrl(url, page)
  await harness.fixture.whenStable()
  return { harness, component, store, root: harness.routeNativeElement as HTMLElement }
}

/** The text of each body row's cells; a count shows its number, not its hover card, and the phone-only machine line is left out. */
export function bodyRows(root: HTMLElement): string[][] {
  return [...root.querySelectorAll('tbody tr')].map((row) => [...row.querySelectorAll('td')].map((cell) => cellText(cell))
  )
}

function cellText(cell: Element): string {
  const copy = cell.cloneNode(true) as Element
  copy.querySelectorAll('.count-card, .narrow-only').forEach((card) => card.remove())
  return (copy.textContent as string).replace(/\s+/g, ' ').trim()
}
