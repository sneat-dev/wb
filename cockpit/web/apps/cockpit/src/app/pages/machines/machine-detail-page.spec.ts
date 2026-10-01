import { TestBed } from '@angular/core/testing'
import { MachineDetailPage } from './machine-detail-page'

describe('MachineDetailPage', () => {
  it('renders its placeholder', async () => {
    const fixture = TestBed.createComponent(MachineDetailPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('not built yet')
  })
})
