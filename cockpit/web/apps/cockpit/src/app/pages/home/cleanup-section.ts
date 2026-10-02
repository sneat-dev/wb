import { ChangeDetectionStrategy, Component, computed, inject, input, signal } from '@angular/core'
import { Router, RouterLink } from '@angular/router'
import { AppLink, Cleanup, chipLink } from '@cockpit/fleet-data'
import { worktreeGc } from '@cockpit/fleet-data/commands'
import { HorizontalBarsSpec } from '@cockpit/ui/chart'
import { ChartView } from '@cockpit/ui/chart'
import { GLYPH_CHECK_CIRCLE, GLYPH_CHEVRON_DOWN, Glyph } from '@cockpit/ui/control'
import { LazyCopy } from './lazy-copy'

/**
 * Home "Cleanup" (REQ:home-cleanup): one line, "N safe to remove; M need a look", with "Review &
 * clean" (Worktrees filtered to the safe set) and, until the cleanup action exists, "Copy
 * template" for the dry run; it expands to the worktree-age bars, each linking to Worktrees with
 * its `age:` term. The counts are indicative and the line says so. The chart is created only when
 * the line is expanded, so Chart.js is not fetched for a page nobody expands.
 */
@Component({
  selector: 'app-cleanup',
  imports: [RouterLink, ChartView, Glyph, LazyCopy],
  templateUrl: './cleanup-section.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CleanupSection {
  readonly cleanup = input.required<Cleanup>()

  private readonly router = inject(Router)
  protected readonly open = signal(false)
  protected readonly ok = GLYPH_CHECK_CIRCLE
  protected readonly chevron = GLYPH_CHEVRON_DOWN
  protected readonly look: AppLink = chipLink('worktrees', 'look')
  protected readonly empty = computed(() => this.cleanup().safeCount === 0 && this.cleanup().lookCount === 0)
  protected readonly spec = computed<HorizontalBarsSpec>(() => ({
    kind: 'horizontal-bars',
    title: 'Worktrees by age of their last activity',
    valueLabel: 'Worktrees',
    bars: this.cleanup().bars.map((bar) => ({ label: bar.label, value: bar.count, link: bar.link })),
  }))
  /** `wb worktree gc`: the dry-run plan of the whole fleet's cleanup (it retires nothing without `--apply`, which no template holds). */
  protected readonly dryRun = async () => worktreeGc()

  protected toggle(): void {
    this.open.update((open) => !open)
  }

  protected go(link: AppLink): void {
    void this.router.navigate([link.path], { queryParams: link.query })
  }
}
