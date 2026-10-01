import { TestBed } from '@angular/core/testing'
import { RepositoryDetailPage } from './repository-detail-page'

describe('RepositoryDetailPage', () => {
  it('renders its placeholder', async () => {
    const fixture = TestBed.createComponent(RepositoryDetailPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('not built yet')
  })
})
