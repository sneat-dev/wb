import { TestBed } from '@angular/core/testing'
import { FETCH } from '@cockpit/fleet-data'
import { settle } from '../pages/test-harness'
import { ReadmeSection } from './readme-section'

interface Pending {
  resolve: (response: Response) => void
  reject: (error: unknown) => void
  url: string
}

/** A fetch whose answers the spec gives when it chooses, so reads can finish out of order. */
function controlledFetch() {
  const pending: Pending[] = []
  const fetcher = vi.fn(
    (input: RequestInfo | URL) =>
      new Promise<Response>((resolve, reject) => {
        pending.push({ resolve, reject, url: String(input) })
      }),
  )
  return { fetcher: fetcher as unknown as typeof fetch, pending, calls: fetcher }
}

function mount(fetcher: typeof fetch, repository: string, canRead: boolean, sessionStatus: 'loading' | 'ready' | 'failed' = 'ready') {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: FETCH, useValue: fetcher }] })
  const fixture = TestBed.createComponent(ReadmeSection)
  fixture.componentRef.setInput('repository', repository)
  fixture.componentRef.setInput('canRead', canRead)
  fixture.componentRef.setInput('sessionStatus', sessionStatus)
  return fixture
}

const text = (root: HTMLElement) => (root.querySelector('section')?.textContent as string).replace(/\s+/g, ' ').trim()

// cockpit#ac:readme-needs-owner
describe('ReadmeSection', () => {
  it('says an owner session is needed, naming wb cockpit, and never asks for the README without one', async () => {
    const { fetcher, calls } = controlledFetch()
    const fixture = mount(fetcher, 'r1', false)
    await fixture.whenStable()
    expect(text(fixture.nativeElement)).toBe('READMEAn owner session is needed to read the README. Run wb cockpit to open one.')
    expect(fixture.nativeElement.querySelector('code').textContent).toBe('wb cockpit')
    expect(calls).not.toHaveBeenCalled()
  })

  it('claims nothing about the session until its read has answered, and says so when it failed', async () => {
    const { fetcher, calls } = controlledFetch()
    const loading = mount(fetcher, 'r1', false, 'loading')
    await loading.whenStable()
    expect(text(loading.nativeElement)).toBe('READMEReading the README…')
    const failed = mount(fetcher, 'r1', false, 'failed')
    await failed.whenStable()
    expect(text(failed.nativeElement)).toContain('The session could not be read')
    expect(text(failed.nativeElement)).toContain('wb cockpit')
    expect(calls).not.toHaveBeenCalled()
  })

  it('reads the README of the repository and renders it', async () => {
    const { fetcher, pending } = controlledFetch()
    const fixture = mount(fetcher, 'repo a', true)
    await fixture.whenStable()
    expect(text(fixture.nativeElement)).toContain('Reading the README…')
    expect(pending.map((read) => read.url)).toEqual(['/api/v1/cockpit/readme?repository=repo%20a'])
    pending[0].resolve(new Response('# Widgets\n\nHello.\n'))
    await settle(fixture, () => fixture.nativeElement.querySelector('h4') !== null)
    expect(fixture.nativeElement.querySelector('app-readme-content h4')?.textContent).toBe('Widgets')
  })

  it('explains each refusal and failure in words, from the daemon short code', async () => {
    const cases: [number, string, string][] = [
      [401, '', 'An owner session is needed'],
      [404, 'readme_not_found', 'no README.md'],
      [403, 'readme_not_a_regular_file', 'not a regular file'],
      [413, 'readme_too_large', '1 MiB'],
      [500, 'read_failed', 'status 500'],
    ]
    for (const [status, code, words] of cases) {
      const { fetcher, pending } = controlledFetch()
      const fixture = mount(fetcher, 'r1', true)
      await fixture.whenStable()
      pending[0].resolve(new Response(JSON.stringify({ error: code }), { status }))
      await settle(fixture, () => !text(fixture.nativeElement).includes('Reading'))
      expect(text(fixture.nativeElement)).toContain(words)
      expect(fixture.nativeElement.querySelector('app-readme-content')).toBeNull()
    }
  })

  it('says the daemon did not answer when the request itself failed', async () => {
    const { fetcher, pending } = controlledFetch()
    const fixture = mount(fetcher, 'r1', true)
    await fixture.whenStable()
    pending[0].reject(new TypeError('network'))
    await settle(fixture, () => !text(fixture.nativeElement).includes('Reading'))
    expect(text(fixture.nativeElement)).toContain('the daemon did not answer')
  })

  it('drops a read that finishes after the repository changed, or the session was lost', async () => {
    const { fetcher, pending } = controlledFetch()
    const fixture = mount(fetcher, 'r1', true)
    await fixture.whenStable()
    fixture.componentRef.setInput('repository', 'r2')
    await fixture.whenStable()
    expect(pending.map((read) => read.url.split('=')[1])).toEqual(['r1', 'r2'])
    pending[1].resolve(new Response('# Second\n'))
    pending[0].resolve(new Response('# First\n'))
    await settle(fixture, () => fixture.nativeElement.querySelector('h4') !== null)
    await new Promise((done) => setTimeout(done, 20))
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('h4')?.textContent).toBe('Second')

    fixture.componentRef.setInput('repository', 'r3')
    await fixture.whenStable()
    fixture.componentRef.setInput('canRead', false)
    await fixture.whenStable()
    pending[2].reject(new TypeError('late'))
    await fixture.whenStable()
    expect(text(fixture.nativeElement)).toContain('An owner session is needed')
    expect(text(fixture.nativeElement)).not.toContain('did not answer')
  })
})
