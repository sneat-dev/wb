import { ChangeDetectionStrategy, Component, effect, inject, input, signal } from '@angular/core'
import { FleetClient, OWNER_SESSION_COMMAND, ReadmeRequestError, SessionStatus, readmeFailureText } from '@cockpit/fleet-data'
import { ReadmeContent } from './readme-content'

type ReadmeState = { kind: 'idle' } | { kind: 'loading' } | { kind: 'failed'; text: string } | { kind: 'ready'; source: string }

/**
 * The README of a repository on this machine. Only a caller holding an owner
 * session reads it: without one the section says so and names the command that
 * provides one, and the README route is never called. The text read is
 * untrusted and goes only to the safe renderer.
 */
@Component({
  selector: 'app-readme-section',
  imports: [ReadmeContent],
  templateUrl: './readme-section.html',
  styleUrl: './readme-section.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ReadmeSection {
  /** The repository's id in the fleet document. */
  readonly repository = input.required<string>()
  /** Whether the session holds the content capability. */
  readonly canRead = input.required<boolean>()
  /** Whether the session read has answered: until it has, nothing is claimed about the session. */
  readonly sessionStatus = input.required<SessionStatus>()

  private readonly client = inject(FleetClient)
  protected readonly state = signal<ReadmeState>({ kind: 'idle' })
  protected readonly command = OWNER_SESSION_COMMAND

  constructor() {
    effect((onCleanup) => {
      const repository = this.repository()
      if (!this.canRead()) {
        this.state.set({ kind: 'idle' })
        return
      }
      // A read answered after the repository or the session changed is dropped.
      let stale = false
      onCleanup(() => (stale = true))
      this.state.set({ kind: 'loading' })
      this.client.readReadme(repository).then(
        (source) => !stale && this.state.set({ kind: 'ready', source }),
        (error: unknown) => {
          if (stale) return
          const failure = error instanceof ReadmeRequestError ? error : new ReadmeRequestError(0, '')
          this.state.set({ kind: 'failed', text: failure.status === 0 ? 'The README could not be read: the daemon did not answer.' : readmeFailureText(failure.status, failure.code) })
        },
      )
    })
  }
}
