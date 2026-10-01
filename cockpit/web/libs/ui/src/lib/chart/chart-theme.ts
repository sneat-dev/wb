/** The colours and font a chart is drawn with, resolved from the design tokens (styles/tokens.css) for the current colour scheme. */
export interface ChartTheme {
  line: string
  fill: string
  bar: string
  barHover: string
  /** The second series of a stacked chart: neutral, so it never reads as a state. */
  barMuted: string
  grid: string
  tick: string
  tooltipBackground: string
  tooltipText: string
  font: string
}

/** The token each colour reads, and what is used where a token is not defined. */
const COLOUR_TOKENS: Record<Exclude<keyof ChartTheme, 'font'>, [token: string, fallback: string]> = {
  line: ['--chart-line', '#3f51d6'],
  fill: ['--chart-fill', 'rgba(63, 81, 214, 0.12)'],
  bar: ['--chart-bar', '#3f51d6'],
  barHover: ['--chart-bar-hover', '#3544b8'],
  barMuted: ['--chart-bar-muted', '#5b6472'],
  grid: ['--chart-grid', '#e2e5ea'],
  tick: ['--chart-tick', '#5e6978'],
  tooltipBackground: ['--chart-tooltip-bg', '#14171c'],
  tooltipText: ['--chart-tooltip-fg', '#ffffff'],
}

/**
 * Resolves the chart tokens under `host`. A token is `light-dark(...)` text that
 * a canvas cannot read, so each is applied to a probe element (through the CSS
 * object model, which the style nonce does not restrict) and read back as the
 * colour the browser computed for the current scheme.
 */
export function readChartTheme(host: HTMLElement, view: Pick<Window, 'getComputedStyle'> = window): ChartTheme {
  const probe = host.ownerDocument.createElement('span')
  host.appendChild(probe)
  const colours = {} as Omit<ChartTheme, 'font'>
  for (const [name, [token, fallback]] of Object.entries(COLOUR_TOKENS) as [keyof typeof COLOUR_TOKENS, [string, string]][]) {
    probe.style.color = ''
    probe.style.color = `var(${token}, ${fallback})`
    colours[name] = view.getComputedStyle(probe).color || fallback
  }
  const font = view.getComputedStyle(host).fontFamily || 'system-ui, sans-serif'
  probe.remove()
  return { ...colours, font }
}
