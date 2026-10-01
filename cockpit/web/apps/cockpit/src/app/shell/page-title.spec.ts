import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { Component } from '@angular/core'
import { DEFAULT_TITLE, PageTitle, providePageTitle } from './page-title'

@Component({ template: '' })
class Blank {}

describe('PageTitleStrategy', () => {
  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideRouter([{ path: 'named', title: 'Named', component: Blank }, { path: 'unnamed', component: Blank }]), providePageTitle()],
    })
  })

  it('sets the document title and the shell title to the route title', async () => {
    const harness = await RouterTestingHarness.create()
    await harness.navigateByUrl('/named')
    expect(document.title).toBe('Named')
    expect(TestBed.inject(PageTitle).title()).toBe('Named')
    expect(TestBed.inject(Router).url).toBe('/named')
  })

  it('falls back to the product name for a route with no title', async () => {
    const harness = await RouterTestingHarness.create()
    await harness.navigateByUrl('/unnamed')
    expect(document.title).toBe(DEFAULT_TITLE)
    expect(TestBed.inject(PageTitle).title()).toBe(DEFAULT_TITLE)
  })
})
