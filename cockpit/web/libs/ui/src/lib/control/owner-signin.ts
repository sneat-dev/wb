import { ChangeDetectionStrategy, Component, output } from '@angular/core'
import { OWNER_SESSION_COMMAND } from '@cockpit/fleet-data'
import { CopyButton } from './copy-button'
import { Glyph } from './glyph'
import { GLYPH_LOCK } from './glyphs'

/**
 * The single place that says how to become an owner (REQ:owner-gating-is-visible):
 * "Sign in as owner: run `wb cockpit`", with the command ready to copy. The session
 * chip shows it for an anonymous reader; no action button carries a sign-in
 * message of its own.
 */
@Component({
  selector: 'app-owner-signin',
  imports: [CopyButton, Glyph],
  template: `
    <p class="ask">
      <app-glyph [paths]="lock" />
      <span>Sign in as owner: run <code>{{ command }}</code></span>
    </p>
    <p class="why">Anonymous readers see fleet metadata. Actions need an owner session.</p>
    <app-copy-button [text]="command" label="Copy wb cockpit: sign in as owner" (copied)="copied.emit($event)" />
  `,
  styles: `
    :host {
      display: flex;
      flex-direction: column;
      gap: var(--space-2);
      align-items: flex-start;
    }
    p {
      margin: 0;
    }
    .ask {
      display: flex;
      gap: var(--space-2);
      align-items: flex-start;
      font-size: var(--fs-sm);
      font-weight: var(--fw-medium);
    }
    .ask app-glyph {
      margin-top: 2px;
      color: var(--text-3);
    }
    code {
      padding: 1px 6px;
      border: 1px solid var(--border);
      border-radius: var(--radius-sm);
      background: var(--surface-sunken);
      font-family: var(--font-mono);
      font-size: var(--fs-xs);
    }
    .why {
      color: var(--text-3);
      font-size: var(--fs-xs);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class OwnerSignIn {
  protected readonly command = OWNER_SESSION_COMMAND
  protected readonly lock = GLYPH_LOCK
  /** Emits the text that was copied. */
  readonly copied = output<string>()
}
