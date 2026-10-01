import { ComponentFixture, TestBed } from '@angular/core/testing'
import { By } from '@angular/platform-browser'
import { Select } from 'primeng/select'
import { FilterBar, FilterChange } from './filter-bar'

async function render(inputs: Record<string, unknown>): Promise<ComponentFixture<FilterBar>> {
  const fixture = TestBed.createComponent(FilterBar)
  for (const [name, value] of Object.entries({ machines: [{ id: 'm-a', label: 'alpha' }, { id: 'm-b', label: 'beta' }], repositories: [{ id: 'r1', label: 'acme/r1' }], ...inputs })) {
    fixture.componentRef.setInput(name, value)
  }
  await fixture.whenStable()
  return fixture
}

const selects = (fixture: ComponentFixture<FilterBar>) => fixture.debugElement.queryAll(By.directive(Select))
const optionsOf = (fixture: ComponentFixture<FilterBar>, index: number) => (selects(fixture)[index].componentInstance as Select).options()

describe('FilterBar', () => {
  it('offers a machine and a repository filter', async () => {
    const fixture = await render({})
    expect(selects(fixture)).toHaveLength(2)
    expect(optionsOf(fixture, 0)).toEqual([{ id: 'm-a', label: 'alpha' }, { id: 'm-b', label: 'beta' }])
    expect(optionsOf(fixture, 1)).toEqual([{ id: 'r1', label: 'acme/r1' }])
  })

  it('leaves the repository filter out when it has no repositories to offer', async () => {
    const fixture = await render({ repositories: null })
    expect(selects(fixture)).toHaveLength(1)
    expect(fixture.componentInstance['repositoryOptions']()).toEqual([])
  })

  it('keeps a filter the URL names even when no option lists it', async () => {
    const fixture = await render({ machine: 'gamma', repository: 'r9' })
    expect(optionsOf(fixture, 0)).toEqual([{ id: 'm-a', label: 'alpha' }, { id: 'm-b', label: 'beta' }, { id: 'gamma', label: 'gamma' }])
    expect(optionsOf(fixture, 1)).toEqual([{ id: 'r1', label: 'acme/r1' }, { id: 'r9', label: 'r9' }])
    const known = await render({ machine: 'm-a', repository: 'r1' })
    expect(optionsOf(known, 0)).toEqual([{ id: 'm-a', label: 'alpha' }, { id: 'm-b', label: 'beta' }])
    expect(optionsOf(known, 1)).toHaveLength(1)
    const none = await render({ repositories: null, machine: 'gamma' })
    expect(optionsOf(none, 0)).toContainEqual({ id: 'gamma', label: 'gamma' })
  })

  it('reports a chosen or cleared filter', async () => {
    const fixture = await render({})
    const changes: FilterChange[] = []
    fixture.componentInstance.changed.subscribe((change) => changes.push(change))
    selects(fixture)[0].triggerEventHandler('ngModelChange', 'beta')
    selects(fixture)[1].triggerEventHandler('ngModelChange', 'r1')
    selects(fixture)[0].triggerEventHandler('ngModelChange', null)
    selects(fixture)[1].triggerEventHandler('ngModelChange', undefined)
    expect(changes).toEqual([
      { key: 'machine', value: 'beta' },
      { key: 'repository', value: 'r1' },
      { key: 'machine', value: null },
      { key: 'repository', value: null },
    ])
  })
})
