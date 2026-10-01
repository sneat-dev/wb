import { ChangeDetectionStrategy, Component, Injectable, computed, inject, input } from '@angular/core'
import { FleetStore, splitRepositoryName } from '@cockpit/fleet-data'

/** The repositories of the document by entry id, and whether the fleet has more than one host; derived once per document. */
@Injectable({ providedIn: 'root' })
export class RepositoryNames {
  private readonly store = inject(FleetStore)
  private readonly byId = computed(() => new Map(this.store.document().repositories.map((repository) => [repository.id, splitRepositoryName(repository)])))
  /** Host names are shown only when this is true (REQ:names-and-times-rendering). */
  readonly multipleHosts = computed(() => new Set([...this.byId().values()].map((parts) => parts.host).filter((host) => host !== undefined)).size > 1)

  /** The host and `owner/name` of a repository entry; the id itself when the document does not list it. */
  of(id: string): { host?: string; slug: string } {
    return this.byId().get(id) ?? { slug: id }
  }
}

/**
 * A repository name: the owner and a slash in muted type, the name in strong
 * type, and the host in front only when the fleet has more than one. Give the
 * repository entry `id`, or its `name` (`owner/name`) and `host`. The full name
 * is the `title`, since a cell truncates.
 */
@Component({
  selector: 'app-repo-name',
  template: `<span class="repo" [attr.title]="full()">@if (prefix()) {<span class="muted">{{ prefix() }}</span>}<strong>{{ strong() }}</strong></span>`,
  styles: `
    .repo {
      display: block;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .muted {
      color: var(--text-3);
    }
    strong {
      font-weight: var(--fw-semibold);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepoName {
  readonly id = input<string>()
  readonly name = input<string>()
  readonly host = input<string>()

  private readonly names = inject(RepositoryNames)
  private readonly parts = computed(() => {
    const id = this.id()
    return id === undefined ? { host: this.host(), slug: this.name() ?? '' } : this.names.of(id)
  })
  private readonly split = computed(() => {
    const { host, slug } = this.parts()
    const slash = slug.indexOf('/') + 1
    const owner = slug.slice(0, slash)
    return { owner: this.names.multipleHosts() && host !== undefined ? `${host}/${owner}` : owner, name: slug.slice(slash) }
  })
  protected readonly prefix = computed(() => this.split().owner)
  protected readonly strong = computed(() => this.split().name)
  protected readonly full = computed(() => `${this.split().owner}${this.split().name}`)
}
