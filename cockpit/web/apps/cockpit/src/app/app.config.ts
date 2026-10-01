import {
  ApplicationConfig,
  provideBrowserGlobalErrorListeners,
  provideZonelessChangeDetection,
} from '@angular/core'
import { provideRouter, withComponentInputBinding } from '@angular/router'
import Aura from '@primeuix/themes/aura'
import { providePrimeNG } from 'primeng/config'
import { appRoutes } from './app.routes'

/**
 * The style nonce the daemon issued for this response. It is carried by the
 * ngCspNonce attribute on the application root, which Angular itself reads for
 * the styles it injects; PrimeNG needs it handed over explicitly.
 */
export function readCspNonce(doc: Document): string | undefined {
  return doc.querySelector('[ngCspNonce]')?.getAttribute('ngCspNonce') ?? undefined
}

export function createAppConfig(doc: Document): ApplicationConfig {
  return {
    providers: [
      provideBrowserGlobalErrorListeners(),
      provideZonelessChangeDetection(),
      provideRouter(appRoutes, withComponentInputBinding()),
      providePrimeNG({
        csp: { nonce: readCspNonce(doc) },
        theme: {
          preset: Aura,
          // 'system' follows the browser's prefers-color-scheme.
          options: { darkModeSelector: 'system' },
        },
      }),
    ],
  }
}
