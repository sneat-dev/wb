import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'

/** A circle as a path, so every glyph is a list of `d` strings. */
function circle(cx: number, cy: number, r: number): string {
  return `M${cx - r} ${cy}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0`
}

const GLYPHS = {
  search: [circle(11, 11, 7), 'm20 20-3.5-3.5'],
  x: ['M6 6l12 12M18 6 6 18'],
  copy: ['M9 9a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-8a2 2 0 0 1-2-2V9Z', 'M5 15V6a2 2 0 0 1 2-2h8'],
  check: ['m5 12.5 4.5 4.5L19 7.5'],
  'arrow-up': ['M12 19V5', 'm6 11 6-6 6 6'],
  'arrow-down': ['M12 5v14', 'm6 13 6 6 6-6'],
  help: [circle(12, 12, 9), 'M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.7.3-1 .9-1 1.7', 'M12 17v.1'],
  external: ['M14 4h6v6', 'M20 4 10 14', 'M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4'],
} as const satisfies Record<string, readonly string[]>

export type GlyphName = keyof typeof GLYPHS

/** The few glyphs the list and the panel draw: 24 px outlines as inline SVG (the content security policy allows no data: image). */
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
      width: var(--icon-size, 1rem);
      height: var(--icon-size, 1rem);
    }
    svg {
      width: 100%;
      height: 100%;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Glyph {
  readonly name = input.required<GlyphName>()
  protected readonly paths = computed(() => GLYPHS[this.name()])
}
