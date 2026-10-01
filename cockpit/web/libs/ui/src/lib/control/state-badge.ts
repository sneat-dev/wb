import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { Glyph } from './glyph'
import { BadgeKind, KIND_NAME, badgeSpec } from './state-vocabulary'

/**
 * One badge for every state vocabulary the interface shows: a colour role, a
 * glyph and a word, in one of two sizes, light and dark. The kind is said ahead
 * of the word for assistive technology ("Task state: ready to land"), so the
 * accessible name always contains the visible text. An absent value is a grey
 * dashed "not reported"; a value outside the vocabulary shows its sanitised raw
 * value, grey and dashed, and is "<kind>: not recognised" to assistive technology.
 */
@Component({
  selector: 'app-state-badge',
  imports: [Glyph],
  template: `<span [class]="'badge tone-' + spec().tone" [class.small]="size() === 'small'" [class.unreported]="spec().unreported" [attr.title]="hint() ?? null">
    <app-glyph [paths]="spec().icon" />
    <span class="visually-hidden">{{ kindName() }}: {{ spec().unrecognised ? 'not recognised' : '' }}</span>
    <span class="text" [attr.aria-hidden]="spec().unrecognised ? 'true' : null">{{ text() }}</span>
  </span>`,
  styleUrl: './state-badge.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class StateBadge {
  readonly kind = input.required<BadgeKind>()
  readonly value = input<string | undefined>()
  /** A word to show instead of the vocabulary's, for example "stale, 3 behind". The glyph and colour still follow the value. */
  readonly label = input<string | undefined>()
  /** `normal` is 24 px high, `small` 20 px (rows of a table). */
  readonly size = input<'normal' | 'small'>('normal')
  /** A tooltip with the detail, for example the exact time. */
  readonly hint = input<string | undefined>()

  protected readonly spec = computed(() => badgeSpec(this.kind(), this.value()))
  protected readonly text = computed(() => this.label() ?? this.spec().label)
  protected readonly kindName = computed(() => KIND_NAME[this.kind()])
}
