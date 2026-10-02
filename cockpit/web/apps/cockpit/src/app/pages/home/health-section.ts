import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { CopyButton, QUOTE_TWICE_HINT, copyName, copyWord } from '@cockpit/ui/control'
import { HealthRow } from './health-rows'

/**
 * Home "Fleet health" (REQ:home-fleet-health): one line per problem with the command that fixes it
 * to copy and where it runs. The host renders it only when there is a problem.
 */
@Component({
  selector: 'app-health',
  imports: [RouterLink, CopyButton],
  templateUrl: './health-section.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HealthSection {
  readonly rows = input.required<readonly HealthRow[]>()

  protected readonly count = computed(() => this.rows().length)
  protected readonly word = copyWord
  protected readonly quoteTwice = QUOTE_TWICE_HINT
  protected name(command: { text: string; needsEdit: boolean }, subject: string): string {
    return copyName(command.needsEdit, command.text, subject)
  }
}
