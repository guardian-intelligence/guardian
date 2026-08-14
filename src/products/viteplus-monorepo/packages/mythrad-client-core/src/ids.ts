// Identity spellings shared by every surface that names a role, a blob,
// or a module: the wire, the session ABI, `/terrain/<hex>`, `/wt-info`,
// and the HUD all use these exact renderings.

/** Roles as they ride the welcome frame. */
export const Role = {
  spectator: 0,
  player: 1,
} as const;

export type RoleName = keyof typeof Role;

/** Renders a u64 blob/hash id the way `/terrain/<hex>` and the HUD spell it. */
export function hex64(v: bigint): string {
  return BigInt.asUintN(64, v).toString(16).padStart(16, "0");
}

/**
 * The `/wt-info` display string for a module: the wire bytes hexed
 * left-to-right. `cw`/`pw` are opaque bytes, so this is a straight
 * transcription — no endianness is involved.
 */
export function moduleHex(bytes: Uint8Array): string {
  return [...bytes].map((n) => n.toString(16).padStart(2, "0")).join("");
}

/**
 * The u32 the session ABI wants for a module word (`session_module_swapped`,
 * `request(2, pw)`): the little-endian load of those same four bytes.
 */
export function moduleWord(bytes: Uint8Array): number {
  if (bytes.length < 4) {
    throw new RangeError(`module word: need 4 bytes, have ${bytes.length}`);
  }
  return new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(0, true);
}

/** Formats a module word that came back from the ABI, by re-storing it little-endian. */
export function moduleWordHex(word: number): string {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, word >>> 0, true);
  return moduleHex(out);
}
