# Print RGB at x,y of a PNG (no PIL): python3 -I scripts/pixel.py shots/arrive.png 400 1300
import sys, zlib, struct
data = open(sys.argv[1], "rb").read()
pos, idat, w, h = 8, b"", 0, 0
while pos < len(data):
    n, typ = struct.unpack(">I4s", data[pos:pos + 8]); body = data[pos + 8:pos + 8 + n]; pos += 12 + n
    if typ == b"IHDR": w, h, bd, ct = struct.unpack(">IIBB", body[:10]); bpp = 4 if ct == 6 else 3
    if typ == b"IDAT": idat += body
raw = zlib.decompress(idat); stride = w * bpp; rows = []; prev = bytearray(stride); i = 0
for _ in range(h):
    f = raw[i]; line = bytearray(raw[i + 1:i + 1 + stride]); i += 1 + stride
    for x in range(stride):
        a = line[x - bpp] if x >= bpp else 0; b = prev[x]; c = prev[x - bpp] if x >= bpp else 0
        if f == 1: line[x] = (line[x] + a) & 255
        elif f == 2: line[x] = (line[x] + b) & 255
        elif f == 3: line[x] = (line[x] + (a + b) // 2) & 255
        elif f == 4:
            p = a + b - c; pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
            line[x] = (line[x] + (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
    rows.append(line); prev = line
for k in range(2, len(sys.argv), 2):
    x, y = int(sys.argv[k]), int(sys.argv[k + 1]); print((x, y), tuple(rows[y][x * bpp:x * bpp + 3]))
