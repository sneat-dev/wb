import { TestBed } from '@angular/core/testing'
import { FleetStore } from '@cockpit/fleet-data'
import { fleetDocument, repository } from '@cockpit/fleet-data/testing'
import { RepoName, RepositoryNames } from './repo-name'

function render(inputs: Record<string, string>, repositories = fleetDocument().repositories) {
  TestBed.inject(FleetStore).document.set(fleetDocument({ repositories }))
  const fixture = TestBed.createComponent(RepoName)
  for (const [name, value] of Object.entries(inputs)) fixture.componentRef.setInput(name, value)
  return fixture.whenStable().then(() => ({
    root: fixture.nativeElement.querySelector('.repo') as HTMLElement,
    muted: fixture.nativeElement.querySelector('.muted')?.textContent,
    strong: fixture.nativeElement.querySelector('strong')?.textContent,
  }))
}

describe('RepoName', () => {
  // cockpit-views#ac:repository-and-time-rendering
  it('shows the owner and a slash muted and the name strong, and no host while the fleet has one host', async () => {
    const one = await render({ id: 'r1' }, [repository('r1', 'alpha'), repository('r3', 'alpha', { name: 'sneat-co/x' })])
    expect(one.muted).toBe('acme/')
    expect(one.strong).toBe('r1')
    expect(one.root.getAttribute('title')).toBe('acme/r1')
  })

  it('adds the host in front when the fleet has more than one', async () => {
    const two = await render({ id: 'r1' }, [repository('r1', 'alpha'), repository('r4', 'alpha', { host: 'gitlab.com' })])
    expect(two.muted).toBe('github.com/acme/')
    expect(two.root.getAttribute('title')).toBe('github.com/acme/r1')
    // A repository with no host of its own shows none, even then.
    const bare = await render({ id: 'r2' }, [repository('r1', 'alpha'), repository('r4', 'alpha', { host: 'gitlab.com' }), repository('r2', 'beta', { host: undefined })])
    expect(bare.muted).toBe('acme/')
  })

  it('takes a name and a host as well as an id, and a name with no owner is all strong', async () => {
    const byName = await render({ name: 'sneat-co/sneat-go', host: 'github.com' })
    expect(byName.muted).toBe('sneat-co/')
    expect(byName.strong).toBe('sneat-go')
    const plain = await render({ name: 'lone' })
    expect(plain.muted).toBeUndefined()
    expect(plain.strong).toBe('lone')
    const nothing = await render({})
    expect(nothing.strong).toBe('')
  })

  it('names a repository the document does not list by its id', async () => {
    const gone = await render({ id: 'r-gone' })
    expect(gone.strong).toBe('r-gone')
    expect(TestBed.inject(RepositoryNames).of('r-gone')).toEqual({ slug: 'r-gone' })
  })
})
