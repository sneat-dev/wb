import { TestBed } from '@angular/core/testing'
import { TaskDetailPage } from './task-detail-page'

describe('TaskDetailPage', () => {
  it('renders its placeholder', async () => {
    const fixture = TestBed.createComponent(TaskDetailPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('not built yet')
  })
})
