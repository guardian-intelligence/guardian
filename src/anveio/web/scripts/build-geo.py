# Regenerates src/deck/geo.ts, the map geometry for the Why Bangladesh figure.
#
# Fetch the sources into one directory first:
#   adm1.geojson  https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/BGD/ADM1/geoBoundaries-BGD-ADM1_simplified.geojson
#   adm3.geojson  https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/BGD/ADM3/geoBoundaries-BGD-ADM3_simplified.geojson
#   rivers.json   Overpass: [out:json];(way["waterway"="river"](22.60,89.18,22.99,89.60);
#                 way["natural"="water"]["water"="river"](22.60,89.18,22.99,89.60););out geom;
#
# Usage: python3 -I scripts/build-geo.py <source-dir> src/deck/geo.ts
import json, math, sys
D = sys.argv[1]; OUT = sys.argv[2]
adm1 = json.load(open(f"{D}/adm1.geojson"))
adm3 = json.load(open(f"{D}/adm3.geojson"))
riv = json.load(open(f"{D}/rivers.json"))

def polys(geom):
    if geom["type"] == "Polygon": return [geom["coordinates"]]
    return geom["coordinates"]

def dp(pts, tol):
    if len(pts) < 3: return pts
    a, b = pts[0], pts[-1]
    dx, dy = b[0]-a[0], b[1]-a[1]; L = math.hypot(dx, dy)
    best, idx = -1, 0
    for i in range(1, len(pts)-1):
        p = pts[i]
        d = abs(dy*p[0]-dx*p[1]+b[0]*a[1]-b[1]*a[0])/L if L else math.hypot(p[0]-a[0], p[1]-a[1])
        if d > best: best, idx = d, i
    if best <= tol: return [a, b]
    return dp(pts[:idx+1], tol)[:-1] + dp(pts[idx:], tol)

def path(rings, proj, tol, close=True):
    out = []
    for r in rings:
        p = dp([proj(*c) for c in r], tol)
        if len(p) < (3 if close else 2): continue
        out.append("M" + " L".join(f"{x:.1f},{y:.1f}" for x, y in p) + (" Z" if close else ""))
    return " ".join(out)

# Country frame: Bangladesh on the upper face of a small sphere, so its
# curvature reads. Offsets from the centre are exaggerated EXAG times and the
# view tilts down from above by TILT degrees.
H = 230.0
EXAG, TILT = 5.0, 32.0
LAT_C, LON_C = 23.7, 90.35
def sphere(lon, lat):
    phi = math.radians(TILT + EXAG*(lat-LAT_C))
    lam = math.radians(EXAG*(lon-LON_C)*math.cos(math.radians(LAT_C)))
    return math.cos(phi)*math.sin(lam), -math.sin(phi)
all_pts = [c for f in adm1["features"] for poly in polys(f["geometry"]) for ring in poly for c in ring]
sp = [sphere(*c) for c in all_pts]
sx0, sx1 = min(p[0] for p in sp), max(p[0] for p in sp)
sy0, sy1 = min(p[1] for p in sp), max(p[1] for p in sp)
s_ = H/(sy1-sy0)
W = (sx1-sx0)*s_
def cproj(lon, lat):
    x, y = sphere(lon, lat)
    return ((x-sx0)*s_, (y-sy0)*s_)
divisions = [path([ring for poly in polys(f["geometry"]) for ring in poly], cproj, 0.3) for f in adm1["features"]]
graticule = []
for lat in range(20, 28):
    graticule.append(path([[(lon/4, lat) for lon in range(4*87, 4*94+1)]], cproj, 0.2, close=False))
for lon in range(87, 94):
    graticule.append(path([[(lon, lat/4) for lat in range(4*20, 4*27+1)]], cproj, 0.2, close=False))

dum = [f for f in adm3["features"] if f["properties"]["shapeName"] == "Dumuria"][0]
dum_rings = [ring for poly in polys(dum["geometry"]) for ring in poly]
dxs = [c[0] for r in dum_rings for c in r]; dys = [c[1] for r in dum_rings for c in r]
dlon0, dlon1, dlat0, dlat1 = min(dxs), max(dxs), min(dys), max(dys)
cx, cy = (dlon0+dlon1)/2, (dlat0+dlat1)/2

# Inset frame: a square around Dumuria with margin.
SIZE = 250.0
half_lat = (dlat1-dlat0)/2*1.3
ik = math.cos(math.radians(cy))
iscale = SIZE/(2*half_lat)
half_lon = half_lat/ik
iproj = lambda lon, lat: ((lon-(cx-half_lon))*ik*iscale, ((cy+half_lat)-lat)*iscale)
box = (cx-half_lon, cy-half_lat, cx+half_lon, cy+half_lat)
def touches(coords):
    return any(box[0]-0.05 <= c[0] <= box[2]+0.05 and box[1]-0.05 <= c[1] <= box[3]+0.05 for c in coords)

neighbours, labels = [], []
for f in adm3["features"]:
    if f is dum: continue
    rings = [ring for poly in polys(f["geometry"]) for ring in poly]
    if any(touches(r) for r in rings):
        neighbours.append(path(rings, iproj, 0.4))
        name = f["properties"]["shapeName"]
        pts = [c for r in rings for c in r]
        mx, my = sum(p[0] for p in pts)/len(pts), sum(p[1] for p in pts)/len(pts)
        x, y = iproj(mx, my)
        if 20 < x < SIZE-20 and 20 < y < SIZE-20: labels.append({"name": name, "x": round(x, 1), "y": round(y, 1)})

lines, areas = [], []
for e in riv["elements"]:
    g = [(p["lon"], p["lat"]) for p in e.get("geometry", [])]
    if not g or not touches(g): continue
    if e["tags"].get("waterway"): lines.append(path([g], iproj, 0.5, close=False))
    else: areas.append(path([g], iproj, 0.5))

# Site 1 (farm) is a stand-in point inside Dumuria about 12 km from Site 3;
# the farm's exact location isn't known.
SITES = {"farm": (89.40, 22.79), "home": (89.387761, 22.8976539)}
# Where deployment lands across the country, one point per region.
TARGETS = {
    "rangpur": (89.25, 25.74), "rajshahi": (88.60, 24.37), "sylhet": (91.87, 24.89),
    "chittagong": (91.78, 22.36), "barisal": (90.37, 22.70), "dumuria": (89.40, 22.80),
    "dhaka": (90.41, 23.81),
}
pt = lambda xy: dict(zip(("x", "y"), map(lambda v: round(v, 1), xy)))
sites = {k: pt(cproj(*v)) for k, v in {**SITES, **TARGETS}.items()}
inset_sites = {k: pt(iproj(*SITES[k])) for k in ("farm", "home")}

# Focus rectangle on the country map, around the inset's extent.
fx0, fy0 = cproj(box[0], box[3]); fx1, fy1 = cproj(box[2], box[1])
km_per_unit = 110.574/iscale

def num(v): return round(v, 1)
data = {
  "country": {"width": num(W), "height": H, "divisions": divisions,
              "graticule": " ".join(g for g in graticule if g), "sites": sites,
              "dumuria": path(dum_rings, cproj, 0.2),
              "focus": {"x": num(fx0), "y": num(fy0), "width": num(fx1-fx0), "height": num(fy1-fy0)}},
  "inset": {"size": SIZE, "kmPerUnit": round(km_per_unit, 5),
            "neighbours": " ".join(neighbours), "riverLines": " ".join(l for l in lines if l),
            "riverAreas": " ".join(a for a in areas if a), "dumuria": path(dum_rings, iproj, 0.3),
            "labels": labels, "sites": inset_sites,
            "dumuriaLabel": {"x": num(iproj(sum(dxs)/len(dxs), sum(dys)/len(dys))[0]), "y": num(iproj(sum(dxs)/len(dxs), sum(dys)/len(dys))[1])}},
}
src = """// Map geometry for the Why Bangladesh figure, projected ahead of time
// (country on an exaggerated sphere, inset equirectangular) so the page ships static
// paths. Regenerate with scripts/build-geo.py rather than editing by hand.
//  - Divisions and upazilas: geoBoundaries gbOpen BGD ADM1 (CC0) and ADM3
//    (BBS/OCHA, CC BY 3.0 IGO).
//  - Rivers: (c) OpenStreetMap contributors (ODbL), via Overpass.
export const GEO = """ + json.dumps(data, ensure_ascii=False, indent=2) + " as const;\n"
open(OUT, "w").write(src)
print("W", num(W), "focus", data["country"]["focus"], "labels", labels, "kmPerUnit", km_per_unit, "bytes", len(src))
