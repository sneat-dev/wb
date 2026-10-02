import { ChangeDetectionStrategy, Component, input } from '@angular/core'
import type { GlyphPaths } from './glyphs'

/**
 * An inline glyph, 1em by default (`--glyph-size`), drawn in the current text
 * colour and hidden from assistive technology. It takes the paths of one glyph
 * (the `GLYPH_*` exports of glyphs.ts), so a page bundles only the glyphs it
 * names. Inline SVG because the content security policy allows no data: image.
 */
@Component({
  selector: 'app-glyph',
  template: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    @for (d of paths(); track $index) {
      <path [attr.d]="d" />
    }
  </svg>`,
  styles: `
    :host {
      display: inline-flex;
      flex: none;
      width: var(--glyph-size, 1em);
      height: var(--glyph-size, 1em);
    }
    svg {
      width: 100%;
      height: 100%;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Glyph {
  readonly paths = input.required<GlyphPaths>()
}
