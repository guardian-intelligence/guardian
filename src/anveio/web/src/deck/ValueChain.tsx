import { accents } from "@guardian/brand/tokens";
import { Wings } from "./Brand";
import { DataMark, dataMarkHalf, type Grade } from "./DataMark";
// What we do, left to right: who records, who manages them, what we add,
// who buys. Video moves right along the chain; money moves back left.

// Line icons (24px grid, Lucide geometry).
const ICONS = {
  hire: (
    <>
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
      <path d="M16 3.13a4 4 0 0 1 0 7.75" />
    </>
  ),
  buyers: (
    <>
      <path d="M6 22V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v18Z" />
      <path d="M6 12H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2" />
      <path d="M18 9h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-2" />
      <path d="M10 6h4M10 10h4M10 14h4M10 18h4" />
    </>
  ),
  record: (
    <>
      <path d="m16 13 5.22 3.48a.5.5 0 0 0 .78-.42V7.87a.5.5 0 0 0-.75-.43L16 10.5" />
      <rect x="2" y="6" width="14" height="12" rx="2" />
    </>
  ),
} as const;

// `money` is what each party is paid; `data` is the grade of data on the leg
// leaving it (one "+" per stage of refinement).
const STATIONS = [
  { kind: "crowd", icon: "record", name: "Individuals", money: "$", data: 1 },
  { kind: "stack", icon: "hire", name: "Agencies", money: "$$", data: 2 },
  { kind: "us", name: null, money: "$$$", data: 3 },
  { kind: "icon", icon: "buyers", name: "Buyers", money: null, data: null },
] as const;

// Illustrative target buyers by category; marks belong to their owners.
const BUYER_GROUPS = [
  { name: "Labs", logos: ["openai.svg", "anthropic.svg"] },
  { name: "Robotics", logos: ["skild.svg"] },
  { name: "Brokers", logos: ["protege.png", "troveo.png", "wirestock.png"] },
] as const;

// Placeholder stills (Wikimedia Commons, Build AI Egocentric-10K) until we have our own footage.
const SAMPLE_GROUPS = [
  { label: "Driving", files: ["driving-1.jpg", "driving-2.jpg", "driving-3.jpg"] },
  { label: "Egocentric", files: ["egocentric-1.jpg", "egocentric-2.jpg", "egocentric-3.jpg"] },
  { label: "Drone", files: ["drone-1.jpg", "drone-2.jpg", "drone-3.jpg"] },
] as const;
export const LOGO_SOURCES = [
  {
    label:
      "OpenAI and Anthropic logos (illustrative targets; trademarks belong to their owners): Simple Icons, CC0",
    url: "https://simpleicons.org",
  },
  {
    label: "AWS logo: Wikimedia Commons",
    url: "https://commons.wikimedia.org/wiki/File:Amazon_Web_Services_Logo.svg",
  },
  {
    label: "Patreon logomark: Wikimedia Commons",
    url: "https://commons.wikimedia.org/wiki/File:Patreon_logomark_2023.svg",
  },
  { label: "MadHive logo: madhive.com", url: "https://www.madhive.com" },
  { label: "Emory University logo: emory.edu", url: "https://www.emory.edu" },
  { label: "Skild AI logo: skild.ai", url: "https://www.skild.ai" },
  { label: "Protege logo: withprotege.ai", url: "https://withprotege.ai" },
  { label: "Troveo logo: troveo.ai", url: "https://www.troveo.ai" },
  { label: "Wirestock logo: wirestock.io", url: "https://wirestock.io" },
  { label: "micro1 logo: micro1.ai", url: "https://www.micro1.ai" },
  { label: "Kled logo: kled.ai", url: "https://www.kled.ai" },
  { label: "Origin Lab logo: originlab.ai", url: "https://originlab.ai" },
  { label: "Mercor logo: mercor.com", url: "https://mercor.com" },
  { label: "Snorkel AI logo: snorkel.ai", url: "https://snorkel.ai" },
  { label: "Handshake logo: joinhandshake.com", url: "https://joinhandshake.com" },
] as const;

export const SAMPLE_SOURCES = [
  {
    label:
      "Driving: A S M Jobaer, inside an auto rickshaw under Dhaka Elevated Expressway, 01 and 02 (CC BY-SA 4.0, cropped)",
    url: "https://commons.wikimedia.org/wiki/File:View_from_inside_an_auto_rickshaw_on_a_rainy_day_under_Dhaka_Elevated_Expressway,_Bangladesh_02.jpg",
  },
  {
    label:
      "Driving: Pratheepps, Autorickshaw view of windshield from inside (public domain, cropped)",
    url: "https://commons.wikimedia.org/wiki/File:Autorickshaw_view_of_windshield_from_inside.jpg",
  },
  {
    label:
      "Egocentric: Build AI, Egocentric-10K evaluation set, Egocentric-10K frames only (Apache-2.0, cropped)",
    url: "https://huggingface.co/datasets/builddotai/Egocentric-10K-Evaluation",
  },
  {
    label: "Drone: Mahinur11, Hands planting hope, Dinajpur (CC BY-SA 4.0, cropped)",
    url: "https://commons.wikimedia.org/wiki/File:Hands_planting_hope.jpg",
  },
  {
    label:
      "Drone: Sultan Ahmed Niloy, Sweet noodles and rice field, Bogura (CC BY-SA 4.0, cropped)",
    url: "https://commons.wikimedia.org/wiki/File:Sweet_noodles_and_rice_field.jpg",
  },
  {
    label: "Drone: Azimronnie, Drying rice 18 (CC BY-SA 4.0, cropped)",
    url: "https://commons.wikimedia.org/wiki/File:Drying_rice_18.jpg",
  },
] as const;

const W = 1184;
const ICON = 32;

// Every element sits on one of these rows, so stations, stems and the three
// branches below line up across the whole diagram.
const NODE_R = 54;
// The two rails mirror each other about the circles, each RAIL_DROP away so
// the data rail can end in an arrow down into Buyers and the money rail in an
// arrow up into Individuals.
const RAIL_DROP = 48;
const ROW = {
  node: 12 + RAIL_DROP + NODE_R,
} as const;
const DATA_Y = ROW.node - NODE_R - RAIL_DROP;
const MONEY_Y = ROW.node + NODE_R + RAIL_DROP;
// Rails sit 12 inside the canvas top and bottom (plus the "$" glyph below).
// The canvas hugs the drawing (the top D marks rise a little above DATA_Y) with
// equal room above and below, so the figure centres where it is placed.
const VIEW_TOP = -11;
const H = MONEY_Y + 12 + 8 - VIEW_TOP;
const ARROW_CLEAR = 12;
// Offset of the out/in money lines from a middle station's centre.
const HOP_TIGHT = 7;
// Size of the D marks on the data hops.
const DATA_MARK = 24;
// Every flow line starts and ends ARROW_CLEAR off a circle, flat across.
const BELOW = ROW.node + NODE_R + ARROW_CLEAR;
const ABOVE = ROW.node - NODE_R - ARROW_CLEAR;
// Stills (left key) are twice the logo size (right key); each key is centred
// on the station row. Stills stay smaller than a station circle (2 x NODE_R).
const TILE = 26;

// The whole figure (sample key, stations, buyer key) is centred: the station
// span is placed so the keys' outer reaches balance about the slide centre.
const KEY_GAP = 10;
const BUYER_LABEL_W = 52;
const BUYER_LOGO = { logo: TILE, gap: 6 };
const most = (counts: number[]) => Math.max(...counts);
// The data key: three card stacks (Driving above, Egocentric and Drone
// below, staggered), a brace gathering them, and an italic "D" beside
// Individuals.
const CARD = { w: 78, h: 54, step: 7, r: 7 };
const STACK_W = CARD.w + 2 * CARD.step;
const STACK_H = CARD.h + 2 * CARD.step;
const STACK_LABEL = 20;
const STACK_COL = 100;
const BRACE = { w: 22, gap: 14 };
const D_LABEL = { w: 40, gap: 16 };
const SAMPLE_REACH =
  NODE_R + D_LABEL.gap + D_LABEL.w + D_LABEL.gap + BRACE.w + BRACE.gap + STACK_COL + STACK_W;
const BUYER_REACH = (() => {
  const n = most(BUYER_GROUPS.map((g) => g.logos.length));
  return NODE_R + KEY_GAP + BUYER_LABEL_W + n * BUYER_LOGO.logo + (n - 1) * BUYER_LOGO.gap;
})();
const flowHalf = (label: string | null) => (label ? label.length * 4.3 : 0);

// Station centres, evenly spaced; the figure is centred.
const STATION_GAP = 190;
const STATION_XS = (() => {
  const span = STATION_GAP * (STATIONS.length - 1);
  const start = (W - span - SAMPLE_REACH - BUYER_REACH) / 2 + SAMPLE_REACH;
  return STATIONS.map((_, i) => start + i * STATION_GAP);
})();

// Agencies: a short stack of coins building up and to the right (the front
// coin on top, the others peeking out down-left). The whole stack fits inside
// a single station circle, so arrows keep the same clearance as elsewhere.
const AGENCY_DEPTH = 3;
const AGENCY_STEP = 5;
const AGENCY_SPREAD = (AGENCY_DEPTH - 1) * AGENCY_STEP;
const AGENCY_R = NODE_R - (AGENCY_SPREAD * Math.SQRT2) / 2;

function CoinStack({
  x,
  y,
  icon,
  label,
}: {
  x: number;
  y: number;
  icon: keyof typeof ICONS;
  label: string;
}) {
  // Coin k (0 = front) sits k steps down-left of the front coin; the front
  // coin is offset up-right so the stack is centred on (x, y).
  const coin = (k: number) => ({
    cx: x + AGENCY_SPREAD / 2 - k * AGENCY_STEP,
    cy: y - AGENCY_SPREAD / 2 + k * AGENCY_STEP,
  });
  const front = coin(0);
  return (
    <g>
      {Array.from({ length: AGENCY_DEPTH }, (_, i) => AGENCY_DEPTH - 1 - i).map((k) => {
        const { cx, cy } = coin(k);
        return (
          <circle
            key={k}
            cx={cx}
            cy={cy}
            r={AGENCY_R}
            className={k === 0 ? "chain-node" : "chain-node chain-node-back"}
          />
        );
      })}
      <svg
        x={front.cx - ICON / 2}
        y={front.cy - ICON / 2 - 9}
        width={ICON}
        height={ICON}
        viewBox="0 0 24 24"
        className="chain-icon"
      >
        {ICONS[icon]}
      </svg>
      <text x={front.cx} y={front.cy + 20} className="chain-name" textAnchor="middle">
        {label}
      </text>
    </g>
  );
}

// Individuals: small piles of squircles building up and to the right (backing
// tiles peek out down-left), each topped with the camera icon. (x, y) of a
// pile is its front tile's top-left.
const CROWD = { tile: 22, radius: 6, step: 3 } as const;

function SquircleStack({ x, y, depth }: { x: number; y: number; depth: number }) {
  const { tile, radius, step } = CROWD;
  const icon = 13;
  return (
    <g>
      {Array.from({ length: depth }, (_, k) => depth - 1 - k).map((d) => (
        <rect
          key={d}
          x={x - d * step}
          y={y + d * step}
          width={tile}
          height={tile}
          rx={radius}
          className={d === 0 ? "chain-node" : "chain-node chain-node-back"}
        />
      ))}
      <svg
        x={x + (tile - icon) / 2}
        y={y + (tile - icon) / 2}
        width={icon}
        height={icon}
        viewBox="0 0 24 24"
        className="chain-icon"
      >
        {ICONS.record}
      </svg>
    </g>
  );
}

// Five piles strewn around the label: two above, three below, staggered so
// the group balances about the station centre. Each is [footprint centre x,
// footprint centre y, depth] relative to the station; footprints stay inside
// the circle's square and clear of the arrows on the centre line.
const CROWD_PILES = [
  [-30, -36, 3],
  [22, -40, 4],
  [-38, 30, 4],
  [4, 38, 5],
  [40, 26, 3],
] as const;

function Crowd({ x, y, label }: { x: number; y: number; label: string }) {
  const { tile, step } = CROWD;
  // A pile's footprint is its front tile grown down-left by the backing tiles;
  // the front tile sits at the footprint's top-right.
  const extent = (n: number) => tile + (n - 1) * step;
  return (
    <g>
      {CROWD_PILES.map(([cx, cy, n], i) => {
        const e = extent(n);
        return <SquircleStack key={i} x={x + cx + e / 2 - tile} y={y + cy - e / 2} depth={n} />;
      })}
      <text x={x} y={y - 2} className="chain-name" textAnchor="middle" dominantBaseline="central">
        {label}
      </text>
    </g>
  );
}

function CardStack({
  x,
  y,
  files,
  label,
}: {
  x: number;
  y: number;
  files: readonly string[];
  label: string;
}) {
  // The front card (the first still) sits top-left and fully visible; the
  // back cards peek out below and to the right. Drawn back to front.
  return (
    <g>
      {[2, 1, 0].map((depth) => {
        const cx = x + depth * CARD.step;
        const cy = y + depth * CARD.step;
        const file = files[depth];
        return (
          <g key={depth}>
            <rect x={cx} y={cy} width={CARD.w} height={CARD.h} rx={CARD.r} className="card-back" />
            {file && (
              <image
                href={`/deck/${file}`}
                x={cx}
                y={cy}
                width={CARD.w}
                height={CARD.h}
                preserveAspectRatio="xMidYMid slice"
                clipPath="url(#chain-thumb)"
              />
            )}
            <rect x={cx} y={cy} width={CARD.w} height={CARD.h} rx={CARD.r} className="card-edge" />
          </g>
        );
      })}
      <text
        x={x + STACK_W / 2}
        y={y + STACK_H + STACK_LABEL - 4}
        className="chain-group"
        textAnchor="middle"
      >
        {label}
      </text>
    </g>
  );
}

// A right-facing curly brace from top to bottom, its point at mid-height.
function Brace({ x, top, bottom }: { x: number; top: number; bottom: number }) {
  const mid = (top + bottom) / 2;
  const half = BRACE.w / 2;
  const r = half;
  const d = [
    `M${x},${top}`,
    `Q${x + half},${top} ${x + half},${top + r}`,
    `L${x + half},${mid - r}`,
    `Q${x + half},${mid} ${x + BRACE.w},${mid}`,
    `Q${x + half},${mid} ${x + half},${mid + r}`,
    `L${x + half},${bottom - r}`,
    `Q${x + half},${bottom} ${x},${bottom}`,
  ].join(" ");
  return <path d={d} className="brace" />;
}

function DataKey({ x, y }: { x: number; y: number }) {
  const circleLeft = x - NODE_R;
  const dRight = circleLeft - D_LABEL.gap;
  const dLeft = dRight - D_LABEL.w;
  const braceX = dLeft - D_LABEL.gap - BRACE.w;
  const right = braceX - BRACE.gap - STACK_W;
  const left = right - STACK_COL;
  const block = STACK_H + STACK_LABEL;
  const rowGap = 14;
  const top = y - (2 * block + rowGap) / 2;
  const lower = top + block + rowGap;
  const [driving, egocentric, drone] = SAMPLE_GROUPS;
  return (
    <g>
      <CardStack x={right - STACK_COL / 2} y={top} files={driving.files} label={driving.label} />
      <CardStack x={left} y={lower} files={egocentric.files} label={egocentric.label} />
      <CardStack x={right} y={lower} files={drone.files} label={drone.label} />
      <Brace x={braceX} top={top} bottom={lower + block} />
      <DataMark x={(dLeft + dRight) / 2} y={y} size={40} grade={0} />
    </g>
  );
}

// A compact key just right of the Buyers circle: one row per category, the
// name and then its logos, centred on the circle.
function BuyerKey({ x, y }: { x: number; y: number }) {
  const { logo, gap } = BUYER_LOGO;
  const row = logo + 10;
  const left = x + NODE_R + KEY_GAP;
  const logosX = left + BUYER_LABEL_W;
  const top = y - (BUYER_GROUPS.length * row) / 2;
  return (
    <g>
      {BUYER_GROUPS.map((g, i) => {
        const cy = top + i * row + row / 2;
        return (
          <g key={g.name}>
            <text x={left} y={cy} className="chain-group" dominantBaseline="central">
              {g.name}
            </text>
            {g.logos.map((file, j) => (
              <image
                key={file}
                href={`/deck/logos/${file}`}
                className="buyer-logo"
                x={logosX + j * (logo + gap)}
                y={cy - logo / 2}
                width={logo}
                height={logo}
              />
            ))}
          </g>
        );
      })}
    </g>
  );
}

const LABEL_PAD = 6;

export function ValueChain() {
  const colX = (i: number) => STATION_XS[i]!;
  const first = colX(0);
  const last = colX(STATIONS.length - 1);
  return (
    <figure className="chain">
      <svg
        viewBox={`0 ${VIEW_TOP} ${W} ${H}`}
        role="img"
        aria-label="Individuals record video for agencies, who deliver it to us; we license it to buyers: labs, robotics firms and data brokers. Money flows back the other way."
      >
        <defs>
          <marker
            id="chain-head"
            viewBox="0 0 10 10"
            refX="8"
            refY="5"
            markerWidth="5"
            markerHeight="5"
            orient="auto"
          >
            <path d="M1,1.5 L8,5 L1,8.5" className="chain-head" />
          </marker>
          <marker
            id="chain-head-strong"
            viewBox="0 0 10 10"
            refX="8"
            refY="5"
            markerWidth="5"
            markerHeight="5"
            orient="auto"
          >
            <path d="M1,1.5 L8,5 L1,8.5" className="chain-head-strong" />
          </marker>
          <clipPath id="chain-thumb" clipPathUnits="objectBoundingBox">
            <rect width="1" height="1" rx="0.05" ry="0.07" />
          </clipPath>
        </defs>

        {/* Data: one hop per handoff, each up from the sender, east, and down
            into the receiver, marked with the grade of data it carries. Over a
            middle station the hops form a tight pair: data in (left), data out
            (right). */}
        {STATIONS.slice(1).map((_, sender) => {
          const receiver = sender + 1;
          const out = colX(sender) + (sender === 0 ? 0 : HOP_TIGHT);
          const into = colX(receiver) - (receiver === STATIONS.length - 1 ? 0 : HOP_TIGHT);
          const mid = (colX(sender) + colX(receiver)) / 2;
          const grade = STATIONS[sender]!.data as Grade;
          // The last handoff, finished data to buyers, is the product: black lines.
          const product = receiver === STATIONS.length - 1;
          const line = product ? "chain-flow chain-flow-strong" : "chain-flow";
          const head = product ? "url(#chain-head-strong)" : "url(#chain-head)";
          const gap = dataMarkHalf(DATA_MARK, grade) + LABEL_PAD;
          return (
            <g key={sender}>
              <path d={`M${out},${ABOVE} V${DATA_Y} H${mid - gap}`} className={line} />
              <path
                d={`M${mid + gap},${DATA_Y} H${into} V${ABOVE}`}
                className={line}
                markerEnd={head}
              />
              <DataMark x={mid} y={DATA_Y} size={DATA_MARK} grade={grade} strong={product} />
            </g>
          );
        })}

        {/* Money: one hop per payment, each down from the payer, west, and up
            into the payee, with the amount halfway along. Under a middle
            station the hops form a tight pair: money out (left), money in
            (right). */}
        {STATIONS.slice(1).map((_, k) => {
          const payer = STATIONS.length - 1 - k;
          const payee = payer - 1;
          const out = colX(payer) - (payer === STATIONS.length - 1 ? 0 : HOP_TIGHT);
          const into = colX(payee) + (payee === 0 ? 0 : HOP_TIGHT);
          const mid = (colX(payer) + colX(payee)) / 2;
          const label = STATIONS[payee]!.money!;
          const gap = flowHalf(label) + LABEL_PAD;
          // Buyers paying us mirrors the product hop above: black lines.
          const product = payer === STATIONS.length - 1;
          const line = product ? "chain-flow chain-flow-strong" : "chain-flow";
          const head = product ? "url(#chain-head-strong)" : "url(#chain-head)";
          return (
            <g key={payer}>
              <path d={`M${out},${BELOW} V${MONEY_Y} H${mid + gap}`} className={line} />
              <path
                d={`M${mid - gap},${MONEY_Y} H${into} V${BELOW}`}
                className={line}
                markerEnd={head}
              />
              <text
                x={mid}
                y={MONEY_Y}
                className={
                  product ? "chain-flow-label chain-flow-label-strong" : "chain-flow-label"
                }
                textAnchor="middle"
                dominantBaseline="central"
              >
                {label}
              </text>
            </g>
          );
        })}

        {STATIONS.map((s, i) => {
          const x = colX(i);
          if (s.kind === "crowd") return <Crowd key={s.name} x={x} y={ROW.node} label={s.name} />;
          if (s.kind === "stack")
            return <CoinStack key={s.name} x={x} y={ROW.node} icon={s.icon} label={s.name} />;
          return (
            <g key={s.name}>
              <circle
                cx={x}
                cy={ROW.node}
                r={NODE_R}
                className="chain-node"
                style={s.kind === "us" ? { fill: accents.ink, stroke: accents.ink } : undefined}
              />
              {s.kind === "us" ? (
                <Wings x={x} y={ROW.node} size={NODE_R * 0.95} />
              ) : (
                <svg
                  x={x - ICON / 2}
                  y={ROW.node - ICON / 2 - 9}
                  width={ICON}
                  height={ICON}
                  viewBox="0 0 24 24"
                  className="chain-icon"
                >
                  {ICONS[s.icon]}
                </svg>
              )}
              {s.name && (
                <text x={x} y={ROW.node + 20} className="chain-name" textAnchor="middle">
                  {s.name}
                </text>
              )}
            </g>
          );
        })}
        <DataKey x={first} y={ROW.node} />

        {/* Buyers: who pays. */}
        <BuyerKey x={last} y={ROW.node} />
      </svg>
    </figure>
  );
}
