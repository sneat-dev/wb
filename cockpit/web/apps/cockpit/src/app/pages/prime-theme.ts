import { APP_INITIALIZER, Type, inject, provideEnvironmentInitializer } from '@angular/core'
import { Routes } from '@angular/router'
import { definePreset } from '@primeuix/themes'
import Aura from '@primeuix/themes/aura'
import { providePrimeNG } from 'primeng/config'
import { readCspNonce } from '../csp-nonce'

/**
 * PrimeNG's Aura theme, re-pointed at the Cockpit's design tokens
 * (styles/tokens.css): its surfaces, text, accent, borders and radii are
 * `var(--...)` references, so a PrimeNG component and a hand-written one share
 * one palette in light and dark, and the tokens stay the single place to change
 * a colour. The semantic tokens of Aura already use light-dark(), which is what
 * the tokens use as well.
 *
 * This file is in the lazy chunk of the page routes (app.routes.ts), never in
 * the initial script. A task that needs a PrimeNG component adds its import to
 * its own page; the theme of every Aura component is available here.
 */
export const CockpitPreset = definePreset(Aura, {
  semantic: {
    primary: {
      color: 'var(--accent)',
      contrastColor: 'var(--on-accent)',
      hoverColor: 'var(--accent-hover)',
      activeColor: 'var(--accent-hover)',
    },
    typography: { fontSize: '0.8125rem', lineHeight: '1.5', fontFamily: 'inherit' },
    focusRing: { width: '2px', style: 'solid', color: 'var(--accent)', offset: '2px' },
    text: { color: 'var(--text)', hoverColor: 'var(--text)', mutedColor: 'var(--text-3)', hoverMutedColor: 'var(--text-2)' },
    content: {
      borderRadius: '{border.radius.lg}',
      background: 'var(--surface)',
      hoverBackground: 'var(--surface-hover)',
      borderColor: 'var(--border)',
      color: 'var(--text)',
      hoverColor: 'var(--text)',
    },
    highlight: {
      background: 'var(--accent-soft)',
      focusBackground: 'var(--accent-soft)',
      color: 'var(--accent)',
      focusColor: 'var(--accent)',
    },
    formField: {
      paddingX: '0.625rem',
      paddingY: '0.3125rem',
      borderRadius: '{border.radius.md}',
      background: 'var(--surface)',
      filledBackground: 'var(--surface-sunken)',
      filledHoverBackground: 'var(--surface-sunken)',
      filledFocusBackground: 'var(--surface)',
      disabledBackground: 'var(--surface-sunken)',
      borderColor: 'var(--border-strong)',
      hoverBorderColor: 'var(--text-3)',
      focusBorderColor: 'var(--accent)',
      color: 'var(--text)',
      disabledColor: 'var(--text-3)',
      placeholderColor: 'var(--text-3)',
      iconColor: 'var(--text-3)',
    },
    list: {
      option: {
        focusBackground: 'var(--surface-hover)',
        selectedBackground: 'var(--accent-soft)',
        selectedFocusBackground: 'var(--accent-soft)',
        color: 'var(--text)',
        focusColor: 'var(--text)',
        selectedColor: 'var(--accent)',
        selectedFocusColor: 'var(--accent)',
      },
      optionGroup: { color: 'var(--text-3)' },
    },
    overlay: {
      select: { background: 'var(--surface-raised)', borderColor: 'var(--border)', color: 'var(--text)', shadow: 'var(--shadow-pop)' },
      popover: { background: 'var(--surface-raised)', borderColor: 'var(--border)', color: 'var(--text)', shadow: 'var(--shadow-pop)' },
      modal: { background: 'var(--surface-raised)', borderColor: 'var(--border)', color: 'var(--text)', shadow: 'var(--shadow-pop)' },
    },
  },
  components: {
    // A dense table: 13 px text, a quiet header, a hairline between rows, no stripes.
    datatable: {
      headerCell: {
        background: 'var(--surface)',
        hoverBackground: 'var(--surface-hover)',
        borderColor: 'var(--border)',
        color: 'var(--text-2)',
        hoverColor: 'var(--text)',
        padding: '0.375rem 0.75rem',
        sm: { padding: '0.375rem 0.75rem' },
      },
      columnTitle: { fontWeight: '500', fontSize: '0.75rem' },
      row: {
        background: 'var(--surface)',
        hoverBackground: 'var(--surface-hover)',
        selectedBackground: 'var(--accent-soft)',
        color: 'var(--text)',
        hoverColor: 'var(--text)',
        selectedColor: 'var(--text)',
        stripedBackground: 'var(--surface-sunken)',
      },
      bodyCell: { borderColor: 'var(--border)', padding: '0.375rem 0.75rem', fontSize: '0.8125rem', selectedBorderColor: 'var(--accent-border)', sm: { padding: '0.375rem 0.75rem' } },
      root: { borderColor: 'var(--border)' },
    },
  },
})

/**
 * `providePrimeNG` configures PrimeNG (theme, style nonce) and runs PrimeNG's own licence
 * verification, with the notice it shows when the licence is not valid, in an application
 * initializer, which Angular runs only at bootstrap. The route that loads PrimeNG is created
 * later, so the initializers its `providePrimeNG` registered are run when the route's injector
 * is created: PrimeNG's own initialisation, licence check included, runs wherever PrimeNG is
 * loaded, and nowhere else.
 */
export function runInitializers(): void {
  for (const initialize of inject(APP_INITIALIZER, { self: true, optional: true }) ?? []) initialize()
}

/**
 * The route of a page that still uses PrimeNG components. This file is itself a lazy
 * chunk, fetched only by those routes (app.routes.ts): the shell and every page that
 * uses no PrimeNG never load PrimeNG, its theme or its licence check.
 */
export function primePage(loadComponent: () => Promise<Type<unknown>>): Routes {
  return [
    {
      path: '',
      providers: [
        providePrimeNG({
          csp: { nonce: readCspNonce(document) },
          // 'system' follows the browser's prefers-color-scheme.
          theme: { preset: CockpitPreset, options: { darkModeSelector: 'system' } },
        }),
        provideEnvironmentInitializer(runInitializers),
      ],
      loadComponent,
    },
  ]
}
