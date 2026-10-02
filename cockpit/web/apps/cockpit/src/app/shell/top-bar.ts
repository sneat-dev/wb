import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, DestroyRef, ElementRef, computed, inject, viewChild } from '@angular/core'
import { NavigationEnd, Router, RouterLink, RouterLinkActive } from '@angular/router'
import { AppLink, BADGE_CAP, FleetStore, badgeLabel, badgeLabelAtLeast, chipLink, countWords } from '@cockpit/fleet-data'
import { filter } from 'rxjs'
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

/** Where a badge's number opens: the list that produced it (REQ:every-number-is-a-link). */
const BADGE_LINK: Record<TabSignal, AppLink> = {
  'needs-you': chipLink('tasks', 'needs-you'),
  running: chipLink('agents', 'running'),
}

/** Scrolls the tab strip the least that shows `tab`, both measured from the strip's own left edge. */
export function scrollTabIntoView(strip: Pick<HTMLElement, 'scrollLeft' | 'clientWidth'>, tab: Pick<HTMLElement, 'offsetLeft' | 'offsetWidth'>): void {
  const margin = 16
  if (tab.offsetLeft < strip.scrollLeft) strip.scrollLeft = Math.max(0, tab.offsetLeft - margin)
  else if (tab.offsetLeft + tab.offsetWidth > strip.scrollLeft + strip.clientWidth) strip.scrollLeft = tab.offsetLeft + tab.offsetWidth - strip.clientWidth + margin
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
  private readonly strip = viewChild.required<ElementRef<HTMLElement>>('strip')
  protected readonly shell = inject(ShellState)
  protected readonly links = PAGE_LINKS
  protected readonly newTaskPath = NEW_TASK_PATH
  protected readonly badgeCap = BADGE_CAP
  protected readonly modifier = modifierLabel(inject(DOCUMENT).defaultView?.navigator)

  /** The signal numbers; undefined until there is data to count. */
  private readonly counts = computed<{ counts: Record<TabSignal, number>; agentsCut: boolean } | undefined>(() => {
    if (!this.store.loaded() || this.store.schemaMismatch() !== null) return undefined
    const model = this.store.model()
    return { counts: { 'needs-you': model.homeBadge, running: model.runningAgentCount }, agentsCut: model.agentsCut }
  })

  /** A badge: the number as shown ("5+" when agents were cut, so the number is a least), what it says in words, and where it opens. */
  protected badge(link: PageLink): { count: number; label: string; words: string; hint: string; hot: boolean; link: AppLink } | undefined {
    const known = this.counts()
    if (link.signal === undefined || known === undefined) return undefined
    const count = known.counts[link.signal]
    // The running agents are counted from the agents each machine sent, which a machine may have cut.
    const atLeast = link.signal === 'running' && known.agentsCut
    return { count, label: atLeast ? badgeLabelAtLeast(count) : badgeLabel(count), words: countWords(count, atLeast), hint: BADGE_HINT[link.signal], hot: count > 0, link: BADGE_LINK[link.signal] }
  }

  private timer: ReturnType<typeof setTimeout> | undefined

  constructor() {
    const destroyed = inject(DestroyRef)
    // On a phone the strip scrolls: after a navigation the current tab is brought into view,
    // once the router has told RouterLinkActive which tab it is.
    const navigated = inject(Router)
      .events.pipe(filter((event) => event instanceof NavigationEnd))
      .subscribe(() => {
        clearTimeout(this.timer)
        this.timer = setTimeout(() => this.revealCurrentTab())
      })
    destroyed.onDestroy(() => {
      navigated.unsubscribe()
      clearTimeout(this.timer)
    })
  }

  private revealCurrentTab(): void {
    const strip = this.strip().nativeElement
    const tab = strip.querySelector<HTMLElement>('.tab.active')
    if (tab) scrollTabIntoView(strip, tab)
  }

  /** The anonymous chip opens the sign-in card; any other session chip is a plain label. Space does not scroll the page. */
  protected ownerHint(space?: Event): void {
    if (!this.session().anonymous) return
    space?.preventDefault()
    this.shell.toggleOwnerHint()
  }

  protected readonly session = computed(() => {
    const principal = this.store.session()?.principal
    if (principal === undefined) {
      return this.store.sessionStatus() === 'failed' ? { text: 'no session', title: 'The session could not be read.', anonymous: false } : { text: 'session', title: 'Reading the session.', anonymous: false }
    }
    return principal === 'owner'
      ? { text: 'owner', title: 'An owner session: this page may read repository content.', anonymous: false }
      : { text: 'anonymous', title: 'An anonymous local reader: fleet metadata only. `wb cockpit` opens an owner session.', anonymous: true }
  })
}
