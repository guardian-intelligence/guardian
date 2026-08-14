// Default port implementations for hosts running on a platform that has
// them. Ports stay interfaces-only in `ports.ts`; this is where a correct
// implementation lives so no host has to write one from scratch and get
// it subtly wrong.

/**
 * The production `random32`: a full 32 bits from the platform CSPRNG.
 *
 * This exists because the obvious wrong answer — `Math.random()`, or a
 * truncated draw — fails silently. The value seeds the intent-id nonce,
 * and since proto 4 dropped `actor` from the event frame, the id is the
 * only handle matching an ack to the intent that earned it. A predictable
 * or repeated nonce means the server's idempotency window (which spans
 * reconnects) swallows a replayed id as a resend, eating a join, and any
 * ack that does land can be credited to the wrong page load's pending
 * intent. Both failures look like the park quietly ignoring the user.
 *
 * Available on any platform with WebCrypto, which includes browsers and
 * Node 19+. Tests inject a seeded generator instead — that is the point
 * of the port — but nothing that talks to a real park may.
 */
export function browserRandom32(): number {
  return crypto.getRandomValues(new Uint32Array(1))[0]!;
}
