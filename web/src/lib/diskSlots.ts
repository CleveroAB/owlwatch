/**
 * Shared, sticky mount → color-slot assignment (DESIGN.md §5.2: color
 * follows the entity, never rank), scoped per server id. One module-level
 * map per server serves the disk chart, the disk tile and the overview
 * card, so a mount keeps its hue across range switches and across the
 * tile/chart boundary — and mounts from different servers never fight
 * over slots.
 *
 * Assignment is first-seen, append-only: seed from the live snapshot's
 * sorted mounts, then new mounts (from live or history data) append in the
 * order they appear. Slots run 1..8; once all 8 are taken, overflow mounts
 * all reuse slot 8.
 */

const MAX_SLOTS = 8;
const serverSlots = new Map<string, Map<string, number>>();

function slotsFor(serverId: string): Map<string, number> {
  let slots = serverSlots.get(serverId);
  if (!slots) {
    slots = new Map<string, number>();
    serverSlots.set(serverId, slots);
  }
  return slots;
}

/**
 * Register a server's mounts in the given order without disturbing existing
 * assignments. Callers pass their mount set sorted so the initial seed is
 * deterministic.
 */
export function registerMounts(serverId: string, mounts: Iterable<string>): void {
  const slots = slotsFor(serverId);
  for (const mount of mounts) {
    if (!slots.has(mount)) {
      slots.set(mount, Math.min(slots.size + 1, MAX_SLOTS));
    }
  }
}

/** CSS color for a mount's slot; unseen mounts are registered on the spot. */
export function mountColor(serverId: string, mount: string): string {
  const slots = slotsFor(serverId);
  if (!slots.has(mount)) registerMounts(serverId, [mount]);
  return `var(--series-${slots.get(mount)!})`;
}
