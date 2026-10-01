import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { FilterNavigator } from './filter-navigator'

describe('FilterNavigator', () => {
  it('sets, merges and drops query parameters on the current page', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([{ path: 'repositories', children: [] }])] })
    const router = TestBed.inject(Router)
    const navigator = TestBed.inject(FilterNavigator)
    await router.navigateByUrl('/repositories?repository=r1')
    expect(await navigator.apply({ key: 'machine', value: 'alpha' })).toBe(true)
    expect(router.url).toBe('/repositories?repository=r1&machine=alpha')
    await navigator.apply({ key: 'repository', value: null })
    expect(router.url).toBe('/repositories?machine=alpha')
  })
})
