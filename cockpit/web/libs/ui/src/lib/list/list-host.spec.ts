import { TestBed } from '@angular/core/testing'
import { Injectable } from '@angular/core'
import { LIST_SHORTCUTS, ListShortcuts, provideListShortcuts } from './list-host'

describe('LIST_SHORTCUTS', () => {
  it('does nothing by default, so a list works where the shell registers none', () => {
    const shortcuts = TestBed.inject(LIST_SHORTCUTS)
    expect(shortcuts.registerFilter({ element: document.createElement('input'), focus: () => undefined, clear: () => undefined, isEmpty: () => true })()).toBeUndefined()
    expect(shortcuts.registerPanel(() => false)()).toBeUndefined()
  })

  it('is the service a page names in provideListShortcuts', () => {
    @Injectable({ providedIn: 'root' })
    class Fake implements ListShortcuts {
      registerFilter = () => () => undefined
      registerPanel = () => () => undefined
    }
    TestBed.configureTestingModule({ providers: [provideListShortcuts(Fake)] })
    expect(TestBed.inject(LIST_SHORTCUTS)).toBe(TestBed.inject(Fake))
  })
})
