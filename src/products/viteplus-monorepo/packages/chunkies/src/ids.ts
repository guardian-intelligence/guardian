// Identity spellings shared by every surface that names a role, a blob,
// or a module: the wire, the session ABI, the blob routes, and every
// pane use these exact renderings.

/** Roles as they ride the welcome frame. */
export const Role = {
  spectator: 0,
  player: 1,
} as const;

export type RoleName = keyof typeof Role;

/** Renders a u64 blob/hash id the way the blob routes spell it. */
export function hex64(v: bigint): string {
  return BigInt.asUintN(64, v).toString(16).padStart(16, "0");
}

/**
 * The display string for a module: the wire bytes hexed left-to-right.
 * `cw`/`pw` are opaque bytes, so this is a straight transcription — no
 * endianness is involved.
 */
export function moduleHex(bytes: Uint8Array): string {
  return [...bytes].map((n) => n.toString(16).padStart(2, "0")).join("");
}

/** Formats a module word that came back from the ABI, by re-storing it little-endian. */
export function moduleWordHex(word: number): string {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, word >>> 0, true);
  return moduleHex(out);
}
