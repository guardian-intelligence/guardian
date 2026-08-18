import { createHmac } from "node:crypto";

const BASE32_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

export function decodeBase32(input: string): Uint8Array {
  const normalized = input.trim().replaceAll(" ", "").replace(/=+$/, "").toUpperCase();
  let bits = 0;
  let value = 0;
  const bytes: number[] = [];
  for (const char of normalized) {
    const index = BASE32_ALPHABET.indexOf(char);
    if (index === -1) {
      throw new Error("TOTP seed is not valid base32");
    }
    value = (value << 5) | index;
    bits += 5;
    if (bits >= 8) {
      bytes.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Uint8Array.from(bytes);
}

export function totp(seed: string, at: Date): string {
  const key = decodeBase32(seed);
  if (key.length < 10) {
    throw new Error("TOTP seed is invalid");
  }
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at.getTime() / 1000 / 30)));
  const digest = createHmac("sha1", Buffer.from(key)).update(counter).digest();
  const offset = digest[digest.length - 1]! & 0x0f;
  const value = digest.readUInt32BE(offset) & 0x7fffffff;
  return String(value % 1_000_000).padStart(6, "0");
}

// Waits out the tail of a 30-second TOTP window so a code is never submitted
// moments before it expires.
export function totpBoundaryDelayMs(at: Date): number {
  const guardMs = 5_000;
  const remaining = 30_000 - (at.getTime() % 30_000);
  if (remaining <= guardMs) {
    return remaining + 1_000;
  }
  return 0;
}
