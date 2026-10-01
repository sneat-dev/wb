import { ChangeDetectionStrategy, Component, ElementRef, computed, effect, inject, input, linkedSignal, signal, untracked } from '@angular/core'
import { RouterLink } from '@angular/router'
import { Branch, FleetClient, FleetRequestError, FleetStore, Machine, RepositoryCheckout, Worktree, taskDetailLink, worktreeDetailLink, routeLabel } from '@cockpit/fleet-data'
import { CodeIndexPanel } from '@cockpit/ui/code-index-panel'
import { MachineChip, StateBadge, SyncBadges } from '@cockpit/ui/control'
import { AgeText } from '@cockpit/ui/list'

/** How long a section waits after it opens before it asks for its branches, so that browsing past a row with j and k asks for none. */
export const BRANCHES_DELAY_MS = 150
/** The branches listed before "Show all": a repository can have hundreds. */
export const BRANCHES_SHOWN = 50

type BranchesState = { kind: 'idle' } | { kind: 'loading' } | { kind: 'failed'; text: string } | { kind: 'ready'; branches: Branch[]; reason?: string }

/**
 * One machine's checkout of a repository (REQ:repository-detail): its facts, its worktrees as
 * compact rows, its code-index panel and its branches, which are read lazily from the branches
 * route when the section is opened (REQ:lazy-branches-route), showing skeleton rows meanwhile, the
 * daemon's reason when it has none to list (a checkout cached from another machine) and an error
 * with a retry.
 */
@Component({
  selector: 'app-repository-machine-section',
  imports: [RouterLink, MachineChip, StateBadge, SyncBadges, AgeText, CodeIndexPanel],
  templateUrl: './repository-machine-section.html',
  styleUrl: './repository-machine-section.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepositoryMachineSection {
  readonly checkout = input.required<RepositoryCheckout>()
  /** This checkout's worktrees. */
  readonly worktrees = input<readonly Worktree[]>([])
  /** The machine entry, for its transport and route; none when the document does not list it. */
  readonly machine = input<Machine>()
  /** Whether the section starts open (the first checkout does). */
  readonly open = input(false)

  protected readonly store = inject(FleetStore)
  private readonly client = inject(FleetClient)
  private readonly element = inject<ElementRef<HTMLElement>>(ElementRef)

  protected readonly expanded = linkedSignal(() => this.open())
  protected readonly branches = signal<BranchesState>({ kind: 'idle' })
  private readonly limit = signal(BRANCHES_SHOWN)
  protected readonly skeleton = [0, 1, 2]
  /** The element id of this machine's section: the fragment a machine chip links with. */
  readonly domId = computed(() => `machine-${this.checkout().machineId}`)
  protected readonly source = computed(() => routeLabel(this.checkout().repository, this.store.now()))
  /** The branches read, once they are; the first of them are listed. */
  private readonly all = signal<readonly Branch[]>([])
  protected readonly listed = computed(() => this.all().slice(0, this.limit()))
  protected readonly hidden = computed(() => Math.max(0, this.all().length - this.limit()))

  constructor() {
    effect((onCleanup) => {
      if (!this.expanded() || untracked(this.branches).kind !== 'idle') return
      const timer = setTimeout(() => void this.load(), BRANCHES_DELAY_MS)
      onCleanup(() => clearTimeout(timer))
    })
  }

  /** Opens the section and brings it into view (a chip of the list named this machine). */
  reveal(): void {
    this.expanded.set(true)
    // After the section has drawn its content, so the scroll lands where the section ends up.
    requestAnimationFrame(() => this.element.nativeElement.scrollIntoView?.({ block: 'start' }))
  }

  protected taskLink = taskDetailLink
  protected worktreeLink = worktreeDetailLink

  protected showAll(): void {
    this.limit.set(Number.MAX_SAFE_INTEGER)
  }

  protected async load(): Promise<void> {
    this.branches.set({ kind: 'loading' })
    try {
      const { branches, reason } = await this.client.readBranches(this.checkout().repository.id)
      this.all.set(branches)
      this.branches.set({ kind: 'ready', branches, reason })
    } catch (error) {
      this.branches.set({ kind: 'failed', text: error instanceof FleetRequestError ? `The daemon answered with status ${error.status}.` : 'The daemon did not answer, or answered with something else.' })
    }
  }
}
