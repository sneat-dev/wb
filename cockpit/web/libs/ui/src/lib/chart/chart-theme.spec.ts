import { readChartTheme } from './chart-theme'

describe('readChartTheme', () => {
  it('reads each token as the colour the browser computed for the current scheme, through a probe it removes again', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const seen: string[] = []
    const view = {
      getComputedStyle: (element: Element) => {
        if (element === host) return { fontFamily: 'Inter, sans-serif', color: '' } as CSSStyleDeclaration
        const color = (element as HTMLElement).style.color
        seen.push(color)
        return { color: `rgb(1, 2, 3) /* ${color} */` } as CSSStyleDeclaration
      },
    }
    const theme = readChartTheme(host, view)
    expect(seen).toHaveLength(10)
    expect(seen.every((value) => value.startsWith('var(--chart-'))).toBe(true)
    expect(theme.line).toContain('var(--chart-line')
    expect(theme.tooltipText).toContain('var(--chart-tooltip-fg')
    expect(theme.barMuted).toContain('var(--chart-bar-muted')
    expect(theme.barSoft).toContain('var(--chart-bar-soft')
    expect(theme.font).toBe('Inter, sans-serif')
    expect(host.children).toHaveLength(0)
  })

  it('falls back to the light palette and the system font where nothing was computed', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const theme = readChartTheme(host, { getComputedStyle: () => ({ color: '', fontFamily: '' }) as CSSStyleDeclaration })
    expect(theme.line).toBe('#3f51d6')
    expect(theme.grid).toBe('#e2e5ea')
    expect([theme.barMuted, theme.barSoft]).toEqual(['#5b6472', '#9aa6ee'])
    expect(theme.font).toBe('system-ui, sans-serif')
  })

  it('uses the window by default', () => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    expect(readChartTheme(host).font).toBeTruthy()
  })
})
