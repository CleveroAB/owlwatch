/**
 * Shared, sticky mount → color-slot assignment (DESIGN.md §5.2: color
 * follows the entity, never rank). One module-level map serves both the
 * disk chart and the disk tile, so a mount keeps its hue across range
 * switches and across the tile/chart boundary.
 *
 * Assignment is first-seen, append-only: seed from the live snapshot's
 * sorted mounts, then new mounts (from live or history data) append in the
 * order they appear. Slots run 1..8; once all 8 are taken, overflow mounts
 * all reuse slot 8.
 */

const MAX_SLOTS = 8;
const mountSlots = new Map<string, number>();

/**
 * Register mounts in the given order without disturbing existing
 * assignments. Callers pass their mount set sorted so the initial seed is
 * deterministic.
 */
export function registerMounts(mounts: Iterable<string>): void {
  for (const mount of mounts) {
    if (!mountSlots.has(mount)) {
      mountSlots.set(mount, Math.min(mountSlots.size + 1, MAX_SLOTS));
    }
  }
}

/** CSS color for a mount's slot; unseen mounts are registered on the spot. */
export function mountColor(mount: string): string {
  if (!mountSlots.has(mount)) registerMounts([mount]);
  return `var(--series-${mountSlots.get(mount)!})`;
}
