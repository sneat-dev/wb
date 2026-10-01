import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'

/**
 * Fixed-height placeholder rows, shown while the daemon is warming up
 * (REQ:look-layout). Their height never depends on the content that replaces
 * them, so data arriving does not move what is above them.
 */
@Component({
  selector: 'app-skeleton-rows',
  template: `<div class="skeleton" aria-hidden="true">
    @for (row of rows(); track row) {
      <div class="skeleton-row"></div>
    }
  </div>`,
  styles: `
    .skeleton {
      display: grid;
      gap: var(--space-2);
    }
    .skeleton-row {
      height: var(--row-h);
      border-radius: var(--radius-md);
      background: linear-gradient(90deg, var(--surface-sunken) 0%, var(--surface-hover) 50%, var(--surface-sunken) 100%);
      background-size: 200% 100%;
      animation: shimmer 1.4s ease-in-out infinite;
    }
    @keyframes shimmer {
      from {
        background-position: 100% 0;
      }
      to {
        background-position: -100% 0;
      }
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SkeletonRows {
  readonly count = input(6)
  protected readonly rows = computed(() => Array.from({ length: this.count() }, (_, index) => index))
}
