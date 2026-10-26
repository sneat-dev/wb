import { signal } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { FETCH, FleetStore } from '@cockpit/fleet-data'
import { AgentSessionStats } from './agent-session-stats'

const bucket = (session_id: string, session_name: string | undefined, repository: string) => ({
  session_id, session_name, repository, runs: 2, completed: 1, failed: 1, pending: 0,
  duration_ms: 1500, usage_runs: 1, input_tokens: 120, output_tokens: 30,
  cost_runs: 1, estimated_usd: 0.25,
})

describe('AgentSessionStats', () => {
  it('shows recorded names prominently, keeps IDs visible, and filters session/repository rows', async () => {
    const report = {
      window_days: 30, total: { ...bucket('', undefined, ''), runs: 3 },
      by_session_repository: [bucket('thread-one', 'builder', 'acme/app'), bucket('thread-two', undefined, 'acme/lib')],
    }
    const fetcher = vi.fn(async (url: string) => new Response(JSON.stringify({ ...report, window_days: Number(url.split('days=')[1]) }), { status: 200 }))
    TestBed.configureTestingModule({ providers: [
      { provide: FleetStore, useValue: { session: signal({ principal: 'owner' }), sessionStatus: signal('ready') } },
      { provide: FETCH, useValue: fetcher },
    ] })
    const fixture = TestBed.createComponent(AgentSessionStats)
    fixture.detectChanges()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(fixture.nativeElement.querySelectorAll('tbody tr')).toHaveLength(2)
    })
    const root = fixture.nativeElement as HTMLElement
    expect(root.querySelector('tbody tr .session strong')?.textContent?.trim()).toBe('builder')
    expect(root.querySelector('tbody tr .session code')?.textContent?.trim()).toBe('thread-one')
    expect(root.textContent).toContain('$0.2500')
    expect(root.textContent).toContain('Tokens reported for 1 of 3 runs')
    expect(root.textContent).toContain('Only WB-dispatched agent runs recorded on this machine')
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/agent-session-stats?days=30', expect.anything())
    const window = root.querySelector<HTMLSelectElement>('#session-usage-window') as HTMLSelectElement
    window.value = '7'
    window.dispatchEvent(new Event('change'))
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(fetcher).toHaveBeenLastCalledWith('/api/v1/cockpit/agent-session-stats?days=7', expect.anything())
      expect(root.querySelectorAll('tbody tr')).toHaveLength(2)
    })
    for (const days of ['30', '90']) {
      window.value = days
      window.dispatchEvent(new Event('change'))
      await vi.waitFor(() => {
        fixture.detectChanges()
        expect(fetcher).toHaveBeenLastCalledWith(`/api/v1/cockpit/agent-session-stats?days=${days}`, expect.anything())
        expect(root.querySelectorAll('tbody tr')).toHaveLength(2)
      })
    }
    const calls = fetcher.mock.calls.length
    window.value = '5'
    window.dispatchEvent(new Event('change'))
    expect(fetcher).toHaveBeenCalledTimes(calls)
    const search = root.querySelector<HTMLInputElement>('input[type=search]') as HTMLInputElement
    search.value = 'acme/lib'
    search.dispatchEvent(new Event('input'))
    fixture.detectChanges()
    expect(root.querySelectorAll('tbody tr')).toHaveLength(1)
    expect(root.querySelector('tbody tr .session strong')?.textContent?.trim()).toBe('thread-two')
  })

  it('does not request private usage for an anonymous reader', async () => {
    const fetcher = vi.fn(async () => new Response('{}', { status: 200 }))
    TestBed.configureTestingModule({ providers: [
      { provide: FleetStore, useValue: { session: signal({ principal: 'anonymous-local' }), sessionStatus: signal('ready') } },
      { provide: FETCH, useValue: fetcher },
    ] })
    const fixture = TestBed.createComponent(AgentSessionStats)
    fixture.detectChanges()
    await fixture.whenStable()
    expect((fixture.componentInstance as unknown as { rows: () => unknown[] }).rows()).toEqual([])
    expect(fetcher).not.toHaveBeenCalled()
    expect((fixture.nativeElement as HTMLElement).textContent).toContain('Sign in as owner')
  })

  it('shows missing telemetry distinctly, formats longer durations, and refreshes after an error', async () => {
    const noUsage = { ...bucket('', undefined, 'acme/app'), duration_ms: 90_000, usage_runs: 0, cost_runs: 0 }
    const long = { ...bucket('thread-one', 'builder', 'acme/lib'), duration_ms: 7_200_000, runs: 1 }
    const report = { window_days: 30, total: { ...noUsage, runs: 3 }, by_session_repository: [noUsage, long] }
    const fetcher = vi.fn()
      .mockRejectedValueOnce(new Error('temporary failure'))
      .mockResolvedValueOnce(new Response(JSON.stringify(report), { status: 200 }))
    TestBed.configureTestingModule({ providers: [
      { provide: FleetStore, useValue: { session: signal({ principal: 'owner' }), sessionStatus: signal('ready') } },
      { provide: FETCH, useValue: fetcher },
    ] })
    const fixture = TestBed.createComponent(AgentSessionStats)
    fixture.detectChanges()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect((fixture.nativeElement as HTMLElement).querySelector('[role=alert]')).not.toBeNull()
    })
    const root = fixture.nativeElement as HTMLElement
    root.querySelector<HTMLButtonElement>('.refresh')?.click()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(root.querySelectorAll('tbody tr')).toHaveLength(2)
    })
    expect(root.querySelector('[role=alert]')).toBeNull()
    expect(root.textContent).toContain('Unattributed')
    expect(root.textContent).toContain('Recorded before caller IDs were captured')
    expect(root.textContent).toContain('1.5 min')
    expect(root.textContent).toContain('2.0 h')
    expect(root.textContent).toContain('0/2 reported')
    expect(root.textContent).toContain('—')
  })

  it('sorts ties by session and repository and ignores a response after owner sign-out', async () => {
    let resolveFirst!: (response: Response) => void
    const first = new Promise<Response>((resolve) => { resolveFirst = resolve })
    const report = {
      window_days: 30, total: { ...bucket('', undefined, ''), runs: 3 },
      by_session_repository: [bucket('thread-one', 'builder', 'acme/z'), bucket('thread-two', undefined, 'acme/a'), bucket('thread-one', undefined, 'acme/a')],
    }
    const fetcher = vi.fn().mockReturnValueOnce(first).mockResolvedValueOnce(new Response(JSON.stringify(report), { status: 200 }))
    const session = signal<{ principal: string } | null>({ principal: 'owner' })
    TestBed.configureTestingModule({ providers: [
      { provide: FleetStore, useValue: { session, sessionStatus: signal('ready') } },
      { provide: FETCH, useValue: fetcher },
    ] })
    const fixture = TestBed.createComponent(AgentSessionStats)
    fixture.detectChanges()
    expect(fetcher).toHaveBeenCalledTimes(1)
    session.set(null)
    fixture.detectChanges()
    resolveFirst(new Response(JSON.stringify(report), { status: 200 }))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect((fixture.componentInstance as unknown as { report: () => unknown }).report()).toBeNull()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect((fixture.nativeElement as HTMLElement).textContent).toContain('Sign in as owner')
    })
    expect((fixture.nativeElement as HTMLElement).querySelector('tbody')).toBeNull()
    session.set({ principal: 'owner' })
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect((fixture.nativeElement as HTMLElement).querySelectorAll('tbody tr')).toHaveLength(3)
    })
    const repositories = [...(fixture.nativeElement as HTMLElement).querySelectorAll('tbody tr td:nth-child(2)')].map((cell) => cell.textContent?.trim())
    expect(repositories).toEqual(['acme/a', 'acme/z', 'acme/a'])
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('keeps the newest refresh when an older response arrives later, and clears figures after a failed refresh', async () => {
    let resolveFirst!: (response: Response) => void
    const first = new Promise<Response>((resolve) => { resolveFirst = resolve })
    const oldReport = { window_days: 30, total: { ...bucket('', undefined, ''), runs: 7 }, by_session_repository: [bucket('old', 'old run', 'acme/old')] }
    const newReport = { window_days: 30, total: { ...bucket('', undefined, ''), runs: 1 }, by_session_repository: [bucket('new', 'new run', 'acme/new')] }
    const fetcher = vi.fn().mockReturnValueOnce(first)
      .mockResolvedValueOnce(new Response(JSON.stringify(newReport), { status: 200 }))
      .mockRejectedValueOnce(new Error('offline'))
    const session = signal<{ principal: string } | null>({ principal: 'owner' })
    TestBed.configureTestingModule({ providers: [
      { provide: FleetStore, useValue: { session, sessionStatus: signal('ready') } },
      { provide: FETCH, useValue: fetcher },
    ] })
    const fixture = TestBed.createComponent(AgentSessionStats)
    fixture.detectChanges()
    const root = fixture.nativeElement as HTMLElement
    session.set(null)
    fixture.detectChanges()
    session.set({ principal: 'owner' })
    fixture.detectChanges()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(root.textContent).toContain('new run')
    })
    resolveFirst(new Response(JSON.stringify(oldReport), { status: 200 }))
    await new Promise((resolve) => setTimeout(resolve, 0))
    fixture.detectChanges()
    expect(root.textContent).not.toContain('old run')
    root.querySelector<HTMLButtonElement>('.refresh')?.click()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(root.querySelector('[role=alert]')).not.toBeNull()
    })
    expect(root.querySelector('tbody')).toBeNull()
  })

  it('ignores a rejected request after sign-out', async () => {
    let rejectFirst!: (error: Error) => void
    const first = new Promise<Response>((_resolve, reject) => { rejectFirst = reject })
    const fetcher = vi.fn().mockReturnValue(first)
    const session = signal<{ principal: string } | null>({ principal: 'owner' })
    TestBed.configureTestingModule({ providers: [
      { provide: FleetStore, useValue: { session, sessionStatus: signal('ready') } },
      { provide: FETCH, useValue: fetcher },
    ] })
    const fixture = TestBed.createComponent(AgentSessionStats)
    fixture.detectChanges()
    session.set(null)
    fixture.detectChanges()
    rejectFirst(new Error('late failure'))
    await new Promise((resolve) => setTimeout(resolve, 0))
    fixture.detectChanges()
    expect((fixture.nativeElement as HTMLElement).querySelector('[role=alert]')).toBeNull()
    expect((fixture.nativeElement as HTMLElement).textContent).toContain('Sign in as owner')
  })
})
