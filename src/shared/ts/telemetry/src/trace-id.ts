// W3C trace ids: 16 random bytes as 32 lowercase hex chars. The hex form
// rides emitSpan attrs ("trace.id") and user-facing diagnostics; the
// base64 form is what the Connect JSON codec expects for proto bytes
// fields (Event.trace_id).

export function newTraceIdHex(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  let hex = "";
  for (const b of bytes) hex += b.toString(16).padStart(2, "0");
  return hex;
}

// Converts a W3C trace id (32 lowercase hex chars) into the base64 the
// Connect JSON codec uses for proto bytes fields (Event.trace_id, 16 bytes).
// Anything else returns "" — a malformed id silently drops rather than
// poisoning the event.
export function traceIdToBase64(hex: string): string {
  if (!/^[0-9a-f]{32}$/.test(hex)) return "";
  let bin = "";
  for (let i = 0; i < 32; i += 2) {
    bin += String.fromCharCode(parseInt(hex.slice(i, i + 2), 16));
  }
  return btoa(bin);
}
