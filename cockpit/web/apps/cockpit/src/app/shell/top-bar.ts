import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { RouterLink, RouterLinkActive } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { NEW_TASK_PATH, PAGE_LINKS, PageLink, TabSignal } from '../nav'
import { modifierLabel } from '../shortcuts/platform'
import { Icon } from '../ui/icon'
import { FreshnessChip } from './freshness-chip'
import { ShellState } from './shell-state'

/** What a tab's badge counts, said for assistive technology. */
const BADGE_HINT: Record<TabSignal, string> = {
  'needs-you': 'tasks need you',
  running: 'agents running',
}

/**
 * The top bar (REQ:top-bar): the brand, the tabs with the two signal badges
 * (Home: tasks in "Needs you"; Agents: running agents), the palette entry,
 * "New task", the freshness chip and the session chip. On a phone the tabs
 * are a scrollable strip below the rest.
 */
@Component({
  selector: 'app-top-bar',
  imports: [RouterLink, RouterLinkActive, Icon, FreshnessChip],
  templateUrl: './top-bar.html',
  styleUrl: './top-bar.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TopBar {
  private readonly store = inject(FleetStore)
  protected readonly shell = inject(ShellState)
  protected readonly links = PAGE_LINKS
  protected readonly newTaskPath = NEW_TASK_PATH
  protected readonly modifier = modifierLabel(inject(DOCUMENT).defaultView?.navigator)

  /** The signal numbers; undefined until there is data to count. */
  private readonly counts = computed<Record<TabSignal, number> | undefined>(() => {
    if (!this.store.loaded() || this.store.schemaMismatch() !== null) return undefined
    const model = this.store.model()
    return { 'needs-you': model.homeBadge, running: model.runningAgentCount }
  })

  protected badge(link: PageLink): { count: number; hint: string; hot: boolean } | undefined {
    const counts = this.counts()
    if (link.signal === undefined || counts === undefined) return undefined
    const count = counts[link.signal]
    return { count, hint: BADGE_HINT[link.signal], hot: count > 0 }
  }

  protected readonly session = computed(() => {
    const principal = this.store.session()?.principal
    if (principal === undefined) {
      return this.store.sessionStatus() === 'failed' ? { text: 'no session', title: 'The session could not be read.' } : { text: 'session', title: 'Reading the session.' }
    }
    return principal === 'owner'
      ? { text: 'owner', title: 'An owner session: this page may read repository content.' }
      : { text: 'anonymous', title: 'An anonymous local reader: fleet metadata only. `wb cockpit` opens an owner session.' }
  })
}
