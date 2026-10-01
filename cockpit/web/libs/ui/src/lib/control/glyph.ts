import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'

/** A circle as a path, so every glyph is a list of `d` strings. */
function circle(cx: number, cy: number, r: number): string {
  return `M${cx - r} ${cy}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0`
}

/**
 * The glyphs of the control surface: 24 px outlines drawn with a 1.75 px
 * stroke in the style of the shell's icons (apps/cockpit/src/app/ui/icon.ts).
 * They are inline SVG because the content security policy allows no data:
 * image. Each state the interface shows has its own shape, so a state is never
 * told by colour alone.
 */
export const GLYPHS = {
  check: ['m5 12.5 4.5 4.5L19 7.5'],
  'check-circle': [circle(12, 12, 9), 'm8 12.5 2.8 2.8L16 9.5'],
  'x-circle': [circle(12, 12, 9), 'm9 9 6 6m0-6-6 6'],
  'minus-circle': [circle(12, 12, 9), 'M8.5 12h7'],
  help: [circle(12, 12, 9), 'M9.6 9.7a2.5 2.5 0 1 1 3.6 2.2c-.8.4-1.2.9-1.2 1.8', 'M12 16.8v.1'],
  ban: [circle(12, 12, 9), 'm5.7 5.7 12.6 12.6'],
  alert: ['M12 4 3 19.5h18L12 4Z', 'M12 10v4.5M12 17.2v.1'],
  'shield-alert': ['M12 3 5 6v5.5c0 4.3 3 7.6 7 9.5 4-1.9 7-5.2 7-9.5V6l-7-3Z', 'M12 8.5v4M12 15.6v.1'],
  clock: [circle(12, 12, 9), 'M12 7v5l3 2'],
  activity: ['M3 12h4l3-8 4 16 3-8h4'],
  radio: [circle(12, 12, 2), 'M7.8 7.8a6 6 0 0 0 0 8.4M16.2 7.8a6 6 0 0 1 0 8.4'],
  archive: ['M3 5h18v4H3V5Z', 'M5 9v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9', 'M10 13h4'],
  server: ['M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 9.5v-4Z', 'M4 14.5A1.5 1.5 0 0 1 5.5 13h13a1.5 1.5 0 0 1 1.5 1.5v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 18.5v-4Z', 'M8 7.5h.1M8 16.5h.1'],
  'git-pull-request': [circle(6, 5.5, 2), circle(6, 18.5, 2), circle(18, 18.5, 2), 'M6 7.5v9M18 16.5V9.5a3 3 0 0 0-3-3h-3', 'm14.5 3.5-2.5 3 2.5 3'],
  'git-merge': [circle(6, 5.5, 2), circle(6, 18.5, 2), circle(18, 12.5, 2), 'M6 7.5v9', 'M8 6c6 0 10 2.5 10 4.5'],
  pencil: ['M4 20h4L19 9l-4-4L4 16v4Z', 'm13.5 6.5 4 4'],
  copy: ['M8 8.5A1.5 1.5 0 0 1 9.5 7h9A1.5 1.5 0 0 1 20 8.5v10a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 8 18.5v-10Z', 'M16 7V5.5A1.5 1.5 0 0 0 14.5 4h-9A1.5 1.5 0 0 0 4 5.5v10A1.5 1.5 0 0 0 5.5 17H8'],
  lock: ['M6 11.5A1.5 1.5 0 0 1 7.5 10h9a1.5 1.5 0 0 1 1.5 1.5v7a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 6 18.5v-7Z', 'M8.5 10V7.5a3.5 3.5 0 0 1 7 0V10'],
  terminal: ['m5 8 4 4-4 4', 'M12 17h7'],
  more: ['M5 12h.1M12 12h.1M19 12h.1'],
  'chevron-down': ['m6 9 6 6 6-6'],
  'arrow-up': ['M12 19V5', 'm6 11 6-6 6 6'],
  'arrow-down': ['M12 5v14', 'm6 13 6 6 6-6'],
  x: ['M6 6l12 12M18 6 6 18'],
} as const satisfies Record<string, readonly string[]>

export type GlyphName = keyof typeof GLYPHS

/** An inline glyph, 1em by default (`--glyph-size`), drawn in the current text colour and hidden from assistive technology. */
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
  readonly name = input.required<GlyphName>()
  protected readonly paths = computed(() => GLYPHS[this.name()])
}
