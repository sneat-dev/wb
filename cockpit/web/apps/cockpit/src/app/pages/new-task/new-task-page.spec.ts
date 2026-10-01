import { TestBed } from '@angular/core/testing'
import { NewTaskPage } from './new-task-page'

describe('NewTaskPage', () => {
  it('renders its placeholder', async () => {
    const fixture = TestBed.createComponent(NewTaskPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('not built yet')
  })
})
