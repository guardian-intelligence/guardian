// A video's length from its MP4/MOV container, read with a handful of ranged
// GETs: walk the top-level box headers to `moov` (phones usually write it
// after the media data), then read `mvhd`'s timescale and duration. Receipts
// count minutes from this rather than from anything the phone reports.

type Read = (start: number, length: number) => Promise<Uint8Array>;

const MAX_TOP_LEVEL_BOXES = 64;
const MOOV_HEAD = 64 * 1024;

export async function mp4Seconds(read: Read, size: number): Promise<number | undefined> {
  let offset = 0;
  for (let i = 0; i < MAX_TOP_LEVEL_BOXES && offset + 8 <= size; i++) {
    const head = view(await read(offset, Math.min(16, size - offset)));
    const box = boxAt(head, 0, size - offset);
    if (!box) return undefined;
    if (box.type === "moov") {
      const moov = view(await read(offset, Math.min(box.size, MOOV_HEAD)));
      return mvhdSeconds(moov, box.header, moov.byteLength);
    }
    offset += box.size;
  }
  return undefined;
}

function mvhdSeconds(moov: DataView, start: number, end: number): number | undefined {
  for (let at = start; at + 8 <= end;) {
    const box = boxAt(moov, at, end - at);
    if (!box) return undefined;
    if (box.type === "mvhd") {
      const body = at + box.header;
      if (body + 32 > end) return undefined;
      const v1 = moov.getUint8(body) === 1;
      const timescale = moov.getUint32(body + (v1 ? 20 : 12));
      const duration = v1 ? Number(moov.getBigUint64(body + 24)) : moov.getUint32(body + 16);
      return timescale > 0 ? duration / timescale : undefined;
    }
    at += box.size;
  }
  return undefined;
}

function boxAt(data: DataView, at: number, remaining: number) {
  if (at + 8 > data.byteLength) return undefined;
  const size32 = data.getUint32(at);
  const type = String.fromCharCode(
    data.getUint8(at + 4),
    data.getUint8(at + 5),
    data.getUint8(at + 6),
    data.getUint8(at + 7),
  );
  if (size32 === 1) {
    if (at + 16 > data.byteLength) return undefined;
    const size = Number(data.getBigUint64(at + 8));
    return size >= 16 ? { type, size, header: 16 } : undefined;
  }
  const size = size32 === 0 ? remaining : size32;
  return size >= 8 ? { type, size, header: 8 } : undefined;
}

const view = (bytes: Uint8Array) => new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
