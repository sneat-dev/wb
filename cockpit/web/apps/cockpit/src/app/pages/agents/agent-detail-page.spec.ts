import { TestBed } from '@angular/core/testing'
import { AgentDetailPage } from './agent-detail-page'

describe('AgentDetailPage', () => {
  it('renders its placeholder', async () => {
    const fixture = TestBed.createComponent(AgentDetailPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('not built yet')
  })
})
