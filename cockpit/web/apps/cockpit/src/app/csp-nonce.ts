/**
 * The style nonce the daemon issued for this response. It is carried by the
 * ngCspNonce attribute on the application root, which Angular itself reads for
 * the styles it injects; PrimeNG needs it handed over explicitly.
 */
export function readCspNonce(doc: Document): string | undefined {
  return doc.querySelector('[ngCspNonce]')?.getAttribute('ngCspNonce') ?? undefined
}
