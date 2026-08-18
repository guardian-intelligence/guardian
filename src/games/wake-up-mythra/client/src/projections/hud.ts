/** Bytes in the `sim_hud` record. */
export const HUD_BYTES = 28;

export const HUD_VERSION = 1;

export type Hud = {
  readonly present: boolean;
  readonly checkedInToday: boolean;
  readonly day: number;
  readonly dogCount: number;
  readonly parkEnergy: bigint;
  readonly selfEnergy: number;
  readonly boosting: boolean;
};

/** Reads a `sim_hud` record. Returns null when the module wrote a version we don't know. */
export function decodeHud(bytes: Uint8Array): Hud | null {
  if (bytes.length < HUD_BYTES) return null;
  const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  if (dv.getUint16(0, true) !== HUD_VERSION) return null;
  return {
    present: dv.getUint8(2) !== 0,
    checkedInToday: dv.getUint8(3) !== 0,
    day: dv.getUint32(4, true),
    dogCount: dv.getUint32(8, true),
    parkEnergy: dv.getBigUint64(12, true),
    selfEnergy: dv.getUint32(20, true),
    boosting: (dv.getUint8(24) & 2) !== 0,
  };
}
