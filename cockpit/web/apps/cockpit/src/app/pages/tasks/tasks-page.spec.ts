import { TestBed } from '@angular/core/testing'
import { TasksPage } from './tasks-page'

describe('TasksPage', () => {
  it('renders its placeholder', async () => {
    const fixture = TestBed.createComponent(TasksPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('not built yet')
  })
})
