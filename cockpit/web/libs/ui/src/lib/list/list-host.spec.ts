import { TestBed } from '@angular/core/testing'
import { Injectable } from '@angular/core'
import { LIST_SHORTCUTS, ListShortcuts } from './list-host'

describe('LIST_SHORTCUTS', () => {
  it('is the service the application names, and has no default', () => {
    @Injectable({ providedIn: 'root' })
    class Fake implements ListShortcuts {
      registerFilter = () => () => undefined
      registerPanel = () => () => undefined
    }
    expect(() => TestBed.inject(LIST_SHORTCUTS)).toThrow()
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: LIST_SHORTCUTS, useExisting: Fake }] })
    expect(TestBed.inject(LIST_SHORTCUTS)).toBe(TestBed.inject(Fake))
  })
})
