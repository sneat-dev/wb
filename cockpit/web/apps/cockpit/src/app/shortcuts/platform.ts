/** The label of the command key: `⌘` on Apple platforms, `Ctrl` elsewhere. */
export function modifierLabel(navigator: Pick<Navigator, 'platform' | 'userAgent'> | undefined): string {
  const text = navigator ? `${navigator.platform} ${navigator.userAgent}` : ''
  return /Mac|iPhone|iPad/.test(text) ? '⌘' : 'Ctrl'
}
