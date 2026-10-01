import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import {
  componentClasses,
  findUnrenderedComponents,
  main,
  rendersComponent,
  stripComments,
  withoutSkipped,
} from './component-specs.mjs'

const component = "@Component({ selector: 'app-x', template: '' })\nexport class X {}\n"
const rendering = 'TestBed.createComponent(X)\n'

let root

function put(file, text) {
  const path = join(root, file)
  mkdirSync(join(path, '..'), { recursive: true })
  writeFileSync(path, text)
}

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), 'component-specs-'))
})

afterEach(() => {
  rmSync(root, { recursive: true, force: true })
})

describe('stripComments', () => {
  it('removes line and block comments but keeps URLs', () => {
    expect(stripComments('a // x\nb /* y\nz */ c\nhttp://h')).toBe('a \nb  c\nhttp://h')
  })
})

describe('componentClasses', () => {
  it('finds a decorator with whitespace before the parenthesis', () => {
    expect(componentClasses("@Component ( { selector: 'a' } )\nexport class A {}")).toEqual(['A'])
  })

  it('finds several components in one file, with other decorators and modifiers', () => {
    const source = `
      @Component({ selector: 'a', template: '<i class="x"></i>' })
      export class A {}
      @Component({ selector: 'b' }) @Other() export default abstract class B {}
      @Component({ selector: 'c' })
      class C {}`
    expect(componentClasses(source)).toEqual(['A', 'B', 'C'])
  })

  it('ignores a commented-out decorator and a decorator with no class', () => {
    expect(componentClasses("// @Component({})\nexport class A {}\n/* @Component({}) class B {} */")).toEqual([])
    expect(componentClasses("@Component({ selector: 'a' })")).toEqual([])
  })
})

describe('withoutSkipped', () => {
  it('removes skipped blocks of every flavour and keeps live ones', () => {
    const text = [
      "it.skip('a', () => { createComponent(A) })",
      "test.skip('b', () => { createComponent(B) })",
      "describe.skip('c', () => { createComponent(C) })",
      "xit('d', () => { createComponent(D) })",
      "xtest('e', () => { createComponent(E) })",
      "xdescribe('f', () => { createComponent(F) })",
      "it('live', () => { createComponent(L) })",
    ].join('\n')
    const live = withoutSkipped(text)
    expect(live).toContain('createComponent(L)')
    for (const name of 'ABCDEF') expect(live).not.toContain(`createComponent(${name})`)
  })

  it('is not fooled by parentheses and escapes inside strings', () => {
    const text = "it.skip(')\\' (' + `)`, () => { createComponent(A) })\nit('x', () => createComponent(L))"
    const live = withoutSkipped(text)
    expect(live).not.toContain('createComponent(A)')
    expect(live).toContain('createComponent(L)')
  })

  it('drops the rest of the text when a skipped call never closes', () => {
    expect(withoutSkipped('it.skip(() => { createComponent(A)')).toBe('')
  })
})

describe('rendersComponent', () => {
  it('requires createComponent of that class, with optional whitespace', () => {
    expect(rendersComponent('TestBed.createComponent( X )', 'X')).toBe(true)
    expect(rendersComponent('TestBed.createComponent(XY)', 'X')).toBe(false)
    expect(rendersComponent('TestBed.createComponent(Y)', 'X')).toBe(false)
  })

  it('does not count comments or skipped tests', () => {
    expect(rendersComponent('// createComponent(X)\n/* createComponent(X) */', 'X')).toBe(false)
    expect(rendersComponent("it.skip('s', () => createComponent(X))", 'X')).toBe(false)
  })
})

describe('findUnrenderedComponents', () => {
  it('accepts a component whose sibling spec renders it', () => {
    put('apps/a/src/x.component.ts', component)
    put('apps/a/src/x.component.spec.ts', rendering)
    expect(findUnrenderedComponents(root)).toEqual([])
  })

  it('reports a component with no sibling spec', () => {
    put('apps/a/src/x.component.ts', component)
    expect(findUnrenderedComponents(root)).toEqual(['apps/a/src/x.component.ts (X)'])
  })

  it('reports a component whose spec renders another class, a comment or a skipped test', () => {
    put('libs/l/src/lib/x.ts', component)
    put('libs/l/src/lib/x.spec.ts', 'TestBed.createComponent(Other)\n')
    put('libs/l/src/lib/y.ts', component.replace('class X', 'class Y'))
    put('libs/l/src/lib/y.spec.ts', '// TestBed.createComponent(Y)\n')
    put('libs/l/src/lib/z.ts', component.replace('class X', 'class Z'))
    put('libs/l/src/lib/z.spec.ts', "it.skip('z', () => TestBed.createComponent(Z))\n")
    expect(findUnrenderedComponents(root)).toEqual([
      'libs/l/src/lib/x.ts (X)',
      'libs/l/src/lib/y.ts (Y)',
      'libs/l/src/lib/z.ts (Z)',
    ])
  })

  it('checks every component in a file separately', () => {
    put('apps/a/src/two.ts', `${component}${component.replace('class X', 'class Y')}`)
    put('apps/a/src/two.spec.ts', 'TestBed.createComponent(X)\n')
    expect(findUnrenderedComponents(root)).toEqual(['apps/a/src/two.ts (Y)'])
  })

  it('looks in every src directory, including a second one next to the first', () => {
    put('libs/a/ui/src/x.ts', component)
    put('libs/a/data/src/y.ts', component.replace('class X', 'class Y'))
    put('apps/b/src/z.ts', component.replace('class X', 'class Z'))
    put('apps/b/tools/src/w.ts', component.replace('class X', 'class W'))
    expect(findUnrenderedComponents(root)).toEqual([
      'apps/b/src/z.ts (Z)',
      'apps/b/tools/src/w.ts (W)',
      'libs/a/data/src/y.ts (Y)',
      'libs/a/ui/src/x.ts (X)',
    ])
  })

  it('ignores non-components, spec and declaration files, and other directories', () => {
    put('apps/a/src/util.ts', 'export const x = 1\n')
    put('apps/a/src/x.spec.ts', component)
    put('apps/a/src/x.d.ts', component)
    put('apps/a/other/x.ts', component)
    put('apps/a/node_modules/p/src/x.ts', component)
    put('node_modules/p/src/x.ts', component)
    expect(findUnrenderedComponents(root)).toEqual([])
  })
})

describe('main', () => {
  it('exits 0 and names the count when every component is rendered', () => {
    put('apps/a/src/x.ts', component)
    put('apps/a/src/x.spec.ts', rendering)
    const lines = []
    expect(main(root, (line) => lines.push(line))).toBe(0)
    expect(lines).toEqual(['component-specs: 1 component, each rendered by its spec'])
  })

  it('exits 1 and lists each unrendered component', () => {
    put('apps/a/src/x.ts', component)
    put('apps/a/src/y.ts', component.replace('class X', 'class Y'))
    const lines = []
    expect(main(root, (line) => lines.push(line))).toBe(1)
    expect(lines.join('\n')).toContain('apps/a/src/x.ts (X)')
    expect(lines.join('\n')).toContain('apps/a/src/y.ts (Y)')
  })

  it('counts several components in the plural', () => {
    put('apps/a/src/x.ts', `${component}${component.replace('class X', 'class Y')}`)
    put('apps/a/src/x.spec.ts', 'createComponent(X)\ncreateComponent(Y)\n')
    const lines = []
    main(root, (line) => lines.push(line))
    expect(lines[0]).toContain('2 components')
  })
})
