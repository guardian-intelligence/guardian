// Anveio Data operating model: 60 monthly periods starting November 2026.
// Pure functions only; the deck renders whatever runModel returns.

export const MONTHS = 60;
export const FIRST_MONTH = { year: 2026, month: 10 } as const; // 0-based month: November

export type Evidence = "sourced" | "derived" | "assumption";
export type Scenario = "bear" | "base" | "bull";

export type AssumptionDef = {
  readonly key: string;
  readonly group: string;
  readonly label: string;
  readonly unit: string;
  readonly values: readonly [bear: number, base: number, bull: number];
  readonly step: number;
  readonly min: number;
  readonly max: number;
  readonly evidence: Evidence;
  readonly note: string;
};

const def = <const K extends string>(
  key: K,
  group: string,
  label: string,
  unit: string,
  values: readonly [number, number, number],
  step: number,
  min: number,
  max: number,
  evidence: Evidence,
  note: string,
): AssumptionDef & { readonly key: K } => ({
  key,
  group,
  label,
  unit,
  values,
  step,
  min,
  max,
  evidence,
  note,
});

export const GROUPS = [
  "Timeline",
  "Collection capacity",
  "Catalogue pricing",
  "Sales channel",
  "Collection cost",
  "Contracts",
  "Overhead",
  "Equipment",
  "Cash and tax",
] as const;

export const ASSUMPTIONS = [
  def(
    "pilotStart",
    "Timeline",
    "Pilot operations begin",
    "month",
    [4, 3, 3],
    1,
    2,
    12,
    "assumption",
    "After the six-week phone-only validation loop (Nov to mid-Dec 2026).",
  ),
  def(
    "firstSale",
    "Timeline",
    "First catalogue sale",
    "month",
    [7, 4, 3],
    1,
    2,
    24,
    "assumption",
    "Continue gate: a paid order or signed LOI by week 6.",
  ),
  def(
    "scaleStart",
    "Timeline",
    "Scale-out in Khulna begins",
    "month",
    [12, 9, 7],
    1,
    3,
    36,
    "assumption",
    "Collector network grows to the scale target over the following 12 months.",
  ),
  def(
    "expandStart",
    "Timeline",
    "National expansion begins",
    "month",
    [30, 25, 19],
    1,
    6,
    48,
    "assumption",
    "Additional districts; reaches the expand target over 24 months.",
  ),
  def(
    "droneStart",
    "Timeline",
    "Drone programme begins",
    "month",
    [61, 9, 6],
    1,
    2,
    61,
    "assumption",
    "Only after a buyer signs for aerial data and CAAB permission clears (45+ days). 61 = never.",
  ),
  def(
    "valHours",
    "Timeline",
    "Validation footage (phones)",
    "hours",
    [12, 15, 20],
    1,
    0,
    100,
    "derived",
    "Demand-validation loop: 10 to 15 hours of first-person phone video.",
  ),

  def(
    "egoP",
    "Collection capacity",
    "Farm-labour collectors, pilot",
    "people",
    [3, 3, 4],
    1,
    0,
    50,
    "assumption",
    "Family farm in Dumuria plus neighbouring pickers.",
  ),
  def(
    "egoS",
    "Collection capacity",
    "Farm-labour collectors, scale",
    "people",
    [12, 25, 40],
    1,
    0,
    500,
    "assumption",
    "Khulna division.",
  ),
  def(
    "egoE",
    "Collection capacity",
    "Farm-labour collectors, expand",
    "people",
    [30, 60, 200],
    1,
    0,
    3000,
    "assumption",
    "Multiple agro-zones.",
  ),
  def(
    "egoHrs",
    "Collection capacity",
    "Hours filmed per collector",
    "hrs / month",
    [30, 40, 50],
    1,
    1,
    200,
    "derived",
    "About 2 hours a day over 20 working days; head-mounted capture during normal work.",
  ),
  def(
    "carP",
    "Collection capacity",
    "Camera cars, pilot",
    "cars",
    [1, 1, 2],
    1,
    0,
    50,
    "assumption",
    "Car-mounted driving video coordinated by the partner.",
  ),
  def(
    "carS",
    "Collection capacity",
    "Camera cars, scale",
    "cars",
    [2, 3, 10],
    1,
    0,
    500,
    "assumption",
    "",
  ),
  def(
    "carE",
    "Collection capacity",
    "Camera cars, expand",
    "cars",
    [3, 6, 80],
    1,
    0,
    3000,
    "assumption",
    "On-spec fleet only. Larger driving fleets are fielded under custom contracts.",
  ),
  def(
    "carHrs",
    "Collection capacity",
    "Hours recorded per car",
    "hrs / month",
    [45, 60, 80],
    1,
    1,
    400,
    "assumption",
    "Cameras ride along on trips the car already makes.",
  ),
  def(
    "droneP",
    "Collection capacity",
    "Drones, pilot",
    "drones",
    [0, 1, 1],
    1,
    0,
    20,
    "assumption",
    "Licensed operator flying fixed plots.",
  ),
  def(
    "droneS",
    "Collection capacity",
    "Drones, scale",
    "drones",
    [0, 2, 3],
    1,
    0,
    100,
    "assumption",
    "",
  ),
  def(
    "droneE",
    "Collection capacity",
    "Drones, expand",
    "drones",
    [0, 2, 12],
    1,
    0,
    500,
    "assumption",
    "",
  ),
  def(
    "droneHrs",
    "Collection capacity",
    "Hours flown per drone",
    "hrs / month",
    [6, 8, 12],
    1,
    1,
    100,
    "derived",
    "Weekly fixed-route missions; about 1 usable hour per flying day.",
  ),

  def(
    "egoPrice",
    "Catalogue pricing",
    "Farm-labour video price",
    "$ / hr / licence",
    [4, 18, 35],
    0.5,
    0,
    200,
    "sourced",
    "Bulk egocentric $2 to $5 per hour; annotated task packs $15 to $50 (StartupFeed, Dexset 2026).",
  ),
  def(
    "carPrice",
    "Catalogue pricing",
    "Driving video price",
    "$ / hr / licence",
    [2, 6, 15],
    0.5,
    0,
    200,
    "assumption",
    "No public price for Bangladesh driving video; a Seoul firm already sells Dhaka rickshaw data.",
  ),
  def(
    "dronePrice",
    "Catalogue pricing",
    "Aerial farm video price",
    "$ / hr / licence",
    [15, 40, 100],
    1,
    0,
    500,
    "sourced",
    "AI labs paid $1 to $4 per minute with drone footage at the premium end (Bloomberg, Jan 2025); creators net about $33/hr (Semafor).",
  ),
  def(
    "egoLic",
    "Catalogue pricing",
    "Licences sold per farm-labour hour",
    "licences",
    [0.6, 1.5, 3],
    0.1,
    0,
    10,
    "assumption",
    "Non-exclusive: the same hour is licensed to several buyers over the window.",
  ),
  def(
    "carLic",
    "Catalogue pricing",
    "Licences sold per driving hour",
    "licences",
    [0.5, 1.2, 2.5],
    0.1,
    0,
    10,
    "assumption",
    "",
  ),
  def(
    "droneLic",
    "Catalogue pricing",
    "Licences sold per aerial hour",
    "licences",
    [0.4, 1, 2],
    0.1,
    0,
    10,
    "assumption",
    "",
  ),
  def(
    "licWindow",
    "Catalogue pricing",
    "Sales window per hour",
    "months",
    [24, 24, 24],
    1,
    1,
    60,
    "assumption",
    "Licences for each accepted hour are spread evenly over this window.",
  ),
  def(
    "erosion",
    "Catalogue pricing",
    "Annual price erosion",
    "% / yr",
    [40, 15, 8],
    1,
    0,
    90,
    "derived",
    "Raw collector rates in India fell about 30% in six months of 2026 (StartupFeed); labelled, rights-cleared hours are assumed to erode slower.",
  ),
  def(
    "egoCap",
    "Catalogue pricing",
    "Farm-labour demand ceiling",
    "$ / yr",
    [150_000, 600_000, 2_000_000],
    10_000,
    0,
    50_000_000,
    "assumption",
    "Most gross catalogue sales the market absorbs per year.",
  ),
  def(
    "carCap",
    "Catalogue pricing",
    "Driving demand ceiling",
    "$ / yr",
    [50_000, 250_000, 1_000_000],
    10_000,
    0,
    50_000_000,
    "assumption",
    "",
  ),
  def(
    "droneCap",
    "Catalogue pricing",
    "Aerial demand ceiling",
    "$ / yr",
    [30_000, 150_000, 500_000],
    10_000,
    0,
    50_000_000,
    "assumption",
    "No buyer was found posting a request for agricultural aerial footage.",
  ),

  def(
    "brokerShare",
    "Sales channel",
    "Sold through brokers",
    "% of sales",
    [80, 60, 40],
    1,
    0,
    100,
    "assumption",
    "Troveo, Defined.ai, Protege. The rest is sold direct.",
  ),
  def(
    "brokerTake",
    "Sales channel",
    "Broker take",
    "% of price",
    [50, 40, 35],
    1,
    0,
    90,
    "derived",
    "Troveo sample contract 60/40 (WSJ, Aug 2025); Datarade free tier 30%.",
  ),
  def(
    "directFee",
    "Sales channel",
    "Direct-sale fees",
    "% of price",
    [5, 4, 3],
    0.5,
    0,
    50,
    "sourced",
    "AWS Data Exchange 3%; Escrow.com 2.6% under $5k.",
  ),
  def(
    "brokerLag",
    "Sales channel",
    "Broker payout delay",
    "months",
    [5, 4, 3],
    1,
    0,
    12,
    "sourced",
    "Troveo pays up to 4 to 5 months after delivery.",
  ),
  def(
    "directLag",
    "Sales channel",
    "Direct payment delay",
    "months",
    [2, 1, 1],
    1,
    0,
    12,
    "assumption",
    "Upfront or milestone terms with 10 to 15 day deemed acceptance.",
  ),

  def(
    "fx",
    "Collection cost",
    "Exchange rate",
    "BDT / $",
    [123, 123, 123],
    0.5,
    50,
    300,
    "sourced",
    "123.18 BDT per USD on 2026-10-07.",
  ),
  def(
    "egoFee",
    "Collection cost",
    "Partner fee, farm-labour video",
    "BDT / hr filmed",
    [650, 500, 450],
    10,
    0,
    10_000,
    "derived",
    "Collector pay of BDT 300 to 400 per hour plus partner margin; farm wage BDT 583/day (BBS).",
  ),
  def(
    "carFee",
    "Collection cost",
    "Partner fee, driving video",
    "BDT / hr recorded",
    [450, 350, 300],
    10,
    0,
    10_000,
    "assumption",
    "Driver is already making the trip.",
  ),
  def(
    "droneFee",
    "Collection cost",
    "Partner fee, aerial video",
    "BDT / hr flown",
    [4_000, 3_000, 2_500],
    50,
    0,
    50_000,
    "derived",
    "Mid cost model about $24 per usable hour.",
  ),
  def(
    "feeInflation",
    "Collection cost",
    "Partner fee increase",
    "% / yr",
    [10, 8, 6],
    0.5,
    0,
    50,
    "assumption",
    "Wage inflation in Bangladesh.",
  ),
  def(
    "egoAcc",
    "Collection cost",
    "Accepted after QC, farm labour",
    "%",
    [55, 70, 85],
    1,
    1,
    100,
    "assumption",
    "Buyers pay per approved hour, not per recorded hour.",
  ),
  def(
    "carAcc",
    "Collection cost",
    "Accepted after QC, driving",
    "%",
    [65, 80, 90],
    1,
    1,
    100,
    "assumption",
    "",
  ),
  def(
    "droneAcc",
    "Collection cost",
    "Accepted after QC, aerial",
    "%",
    [60, 75, 85],
    1,
    1,
    100,
    "derived",
    "60 to 80% QC pass rate assumed in the cost model.",
  ),
  def(
    "egoProc",
    "Collection cost",
    "Labelling and rights, farm labour",
    "$ / accepted hr",
    [4, 3, 2.5],
    0.25,
    0,
    100,
    "derived",
    "Task labels, manifests, per-clip consent; farm logs attached.",
  ),
  def(
    "carProc",
    "Collection cost",
    "Labelling and privacy, driving",
    "$ / accepted hr",
    [3.5, 2.5, 2],
    0.25,
    0,
    100,
    "assumption",
    "Face and number-plate blurring, QC.",
  ),
  def(
    "droneProc",
    "Collection cost",
    "Labelling and georeference, aerial",
    "$ / accepted hr",
    [6, 5, 4],
    0.25,
    0,
    100,
    "assumption",
    "",
  ),
  def(
    "egoGb",
    "Collection cost",
    "Storage, farm labour",
    "GB / hr",
    [12, 10, 8],
    1,
    0,
    200,
    "derived",
    "1080p to 2.7K head-mounted video.",
  ),
  def(
    "carGb",
    "Collection cost",
    "Storage, driving",
    "GB / hr",
    [14, 12, 10],
    1,
    0,
    200,
    "derived",
    "",
  ),
  def(
    "droneGb",
    "Collection cost",
    "Storage, aerial",
    "GB / hr",
    [45, 45, 45],
    1,
    0,
    200,
    "derived",
    "4K at 100 Mbps is about 45 GB per hour.",
  ),
  def(
    "tbCost",
    "Collection cost",
    "Object storage",
    "$ / TB / month",
    [7, 6, 6],
    0.5,
    0,
    100,
    "sourced",
    "Backblaze B2 list price.",
  ),

  def(
    "cStart",
    "Contracts",
    "Custom collection contracts begin",
    "month",
    [18, 12, 9],
    1,
    1,
    61,
    "assumption",
    "Commissioned capture for robotics labs and capture vendors. 61 = never.",
  ),
  def(
    "cPerYear",
    "Contracts",
    "Custom contracts in first year",
    "per yr",
    [1, 3, 4],
    0.5,
    0,
    100,
    "assumption",
    "",
  ),
  def(
    "cGrowth",
    "Contracts",
    "Custom contract growth",
    "% / yr",
    [30, 60, 80],
    5,
    0,
    500,
    "assumption",
    "",
  ),
  def(
    "cValue",
    "Contracts",
    "Average custom contract",
    "$",
    [30_000, 60_000, 90_000],
    5_000,
    0,
    10_000_000,
    "assumption",
    "Mecka and Human Archive show labs paying for commissioned human-data capture.",
  ),
  def(
    "cMargin",
    "Contracts",
    "Custom contract gross margin",
    "%",
    [40, 55, 65],
    1,
    0,
    100,
    "assumption",
    "",
  ),
  def(
    "gStart",
    "Contracts",
    "Ground-truth programmes begin",
    "month",
    [24, 15, 12],
    1,
    1,
    61,
    "assumption",
    "Insurers and Earth-observation groups. 61 = never.",
  ),
  def(
    "gPerYear",
    "Contracts",
    "Ground-truth programmes in first year",
    "per yr",
    [1, 2, 3],
    0.5,
    0,
    100,
    "assumption",
    "SENA, BRAC/Pula, Green Delta, ICIMOD/SERVIR, IRRI/BRRI.",
  ),
  def(
    "gGrowth",
    "Contracts",
    "Ground-truth programme growth",
    "% / yr",
    [20, 50, 60],
    5,
    0,
    500,
    "assumption",
    "",
  ),
  def(
    "gValue",
    "Contracts",
    "Average ground-truth programme",
    "$",
    [15_000, 25_000, 40_000],
    1_000,
    0,
    10_000_000,
    "assumption",
    "Priced per verified plot or per event.",
  ),
  def(
    "gMargin",
    "Contracts",
    "Ground-truth gross margin",
    "%",
    [40, 55, 60],
    1,
    0,
    100,
    "assumption",
    "",
  ),
  def(
    "contractLag",
    "Contracts",
    "Contract payment delay",
    "months",
    [2, 1, 1],
    1,
    0,
    12,
    "assumption",
    "Milestone billing.",
  ),

  def(
    "legal",
    "Overhead",
    "Legal, accounting, Delaware",
    "$ / month",
    [800, 600, 600],
    50,
    0,
    50_000,
    "assumption",
    "",
  ),
  def(
    "perContractLegal",
    "Overhead",
    "Legal review per contract",
    "$",
    [2_500, 2_000, 1_500],
    100,
    0,
    100_000,
    "derived",
    "Flat-fee data licence review $300 to $1,500 (estimate).",
  ),
  def(
    "software",
    "Overhead",
    "Software and tooling",
    "$ / month",
    [250, 200, 200],
    25,
    0,
    50_000,
    "assumption",
    "",
  ),
  def(
    "insurance",
    "Overhead",
    "Insurance and compliance",
    "$ / month",
    [400, 300, 300],
    25,
    0,
    50_000,
    "assumption",
    "Drone liability, CAAB filings, consent audits.",
  ),
  def(
    "travel",
    "Overhead",
    "Travel to Bangladesh",
    "$ / yr",
    [6_000, 5_000, 5_000],
    500,
    0,
    500_000,
    "assumption",
    "",
  ),
  def(
    "opsStart",
    "Overhead",
    "Data operations lead joins",
    "month",
    [12, 10, 8],
    1,
    1,
    61,
    "assumption",
    "Bangladesh-based, remote.",
  ),
  def(
    "opsSalary",
    "Overhead",
    "Data operations lead",
    "$ / month",
    [2_500, 2_500, 2_500],
    100,
    0,
    50_000,
    "assumption",
    "",
  ),
  def(
    "salesStart",
    "Overhead",
    "Head of data sales joins",
    "month",
    [30, 19, 15],
    1,
    1,
    61,
    "assumption",
    "US-based.",
  ),
  def(
    "salesSalary",
    "Overhead",
    "Head of data sales",
    "$ / month",
    [10_000, 10_000, 10_000],
    500,
    0,
    100_000,
    "assumption",
    "",
  ),
  def(
    "founderStart",
    "Overhead",
    "Founder salary begins",
    "month",
    [36, 25, 19],
    1,
    1,
    61,
    "assumption",
    "",
  ),
  def(
    "founderSalary",
    "Overhead",
    "Founder salary",
    "$ / month",
    [6_000, 8_000, 10_000],
    500,
    0,
    100_000,
    "assumption",
    "",
  ),
  def(
    "load",
    "Overhead",
    "Payroll load",
    "%",
    [20, 20, 20],
    1,
    0,
    100,
    "assumption",
    "Taxes, benefits, contractor fees.",
  ),

  def(
    "camStart",
    "Equipment",
    "Head cameras replace phones",
    "month",
    [12, 9, 7],
    1,
    1,
    61,
    "assumption",
    "Phones until scale-out.",
  ),
  def(
    "camCost",
    "Equipment",
    "Head camera",
    "$ each",
    [150, 120, 120],
    10,
    0,
    5_000,
    "assumption",
    "",
  ),
  def(
    "dashCost",
    "Equipment",
    "Car camera rig",
    "$ each",
    [200, 180, 180],
    10,
    0,
    5_000,
    "assumption",
    "",
  ),
  def(
    "droneCost",
    "Equipment",
    "Drone kit with spares",
    "$ each",
    [1_500, 1_300, 1_300],
    50,
    0,
    50_000,
    "derived",
    "Mini 5 Pro Fly More plus RC2 BDT 109,999 (Star Tech) plus spare batteries.",
  ),
  def(
    "life",
    "Equipment",
    "Equipment life",
    "months",
    [18, 24, 30],
    1,
    1,
    120,
    "assumption",
    "Straight-line depreciation; replacements bought at the same rate.",
  ),

  def(
    "taxRate",
    "Cash and tax",
    "Income tax",
    "%",
    [21, 21, 21],
    1,
    0,
    60,
    "sourced",
    "US federal corporate rate; losses carried forward.",
  ),
  def(
    "openingCash",
    "Cash and tax",
    "Opening cash",
    "$",
    [0, 0, 0],
    1_000,
    0,
    100_000_000,
    "assumption",
    "Zero, so the low point of the cash curve is the capital required.",
  ),
] as const satisfies readonly AssumptionDef[];

export type AssumptionKey = (typeof ASSUMPTIONS)[number]["key"];
export type Inputs = Record<AssumptionKey, number>;

const SCENARIO_INDEX = { bear: 0, base: 1, bull: 2 } as const satisfies Record<Scenario, number>;

export const scenarioInputs = (scenario: Scenario): Inputs =>
  Object.fromEntries(ASSUMPTIONS.map((a) => [a.key, a.values[SCENARIO_INDEX[scenario]]])) as Inputs;

export const STREAMS = [
  { id: "ego", name: "Farm-labour video", short: "Farm labour" },
  { id: "car", name: "Driving video", short: "Driving" },
  { id: "drone", name: "Aerial farm video", short: "Aerial" },
] as const;
export type StreamId = (typeof STREAMS)[number]["id"];

export const LINES = [
  { id: "ego", name: "Farm-labour licences" },
  { id: "car", name: "Driving licences" },
  { id: "drone", name: "Aerial licences" },
  { id: "custom", name: "Custom collection" },
  { id: "truth", name: "Ground-truth programmes" },
] as const;
export type LineId = (typeof LINES)[number]["id"];

type StreamParams = {
  readonly units: readonly [pilot: number, scale: number, expand: number];
  readonly hoursPerUnit: number;
  readonly startMonth: number;
  readonly price: number;
  readonly licences: number;
  readonly cap: number;
  readonly fee: number;
  readonly acceptance: number;
  readonly processing: number;
  readonly gb: number;
  readonly equipment: number;
  readonly equipmentStart: number;
};

const streamParams = (a: Inputs): Record<StreamId, StreamParams> => ({
  ego: {
    units: [a.egoP, a.egoS, a.egoE],
    hoursPerUnit: a.egoHrs,
    startMonth: a.pilotStart,
    price: a.egoPrice,
    licences: a.egoLic,
    cap: a.egoCap,
    fee: a.egoFee,
    acceptance: a.egoAcc / 100,
    processing: a.egoProc,
    gb: a.egoGb,
    equipment: a.camCost,
    equipmentStart: a.camStart,
  },
  car: {
    units: [a.carP, a.carS, a.carE],
    hoursPerUnit: a.carHrs,
    startMonth: a.pilotStart,
    price: a.carPrice,
    licences: a.carLic,
    cap: a.carCap,
    fee: a.carFee,
    acceptance: a.carAcc / 100,
    processing: a.carProc,
    gb: a.carGb,
    equipment: a.dashCost,
    equipmentStart: a.pilotStart,
  },
  drone: {
    units: [a.droneP, a.droneS, a.droneE],
    hoursPerUnit: a.droneHrs,
    startMonth: a.droneStart,
    price: a.dronePrice,
    licences: a.droneLic,
    cap: a.droneCap,
    fee: a.droneFee,
    acceptance: a.droneAcc / 100,
    processing: a.droneProc,
    gb: a.droneGb,
    equipment: a.droneCost,
    equipmentStart: a.droneStart,
  },
});

// Piecewise-linear ramp through the phase milestones; times are forced
// non-decreasing so edited timelines never produce a backwards segment.
export const rampUnits = (
  a: Inputs,
  units: readonly [number, number, number],
  t: number,
): number => {
  const raw: Array<readonly [number, number]> = [
    [a.pilotStart, units[0]],
    [a.scaleStart, units[0]],
    [a.scaleStart + 12, units[1]],
    [a.expandStart, units[1]],
    [a.expandStart + 24, units[2]],
  ];
  const points: Array<readonly [number, number]> = [];
  for (const [time, value] of raw) {
    const previous = points.at(-1);
    points.push([previous ? Math.max(previous[0], time) : time, value]);
  }
  const first = points[0];
  const last = points.at(-1);
  if (!first || !last) return 0;
  if (t < first[0]) return 0;
  for (let i = 0; i + 1 < points.length; i++) {
    const [t0, v0] = points[i]!;
    const [t1, v1] = points[i + 1]!;
    if (t <= t1) return t1 === t0 ? v1 : v0 + ((v1 - v0) * (t - t0)) / (t1 - t0);
  }
  return last[1];
};

const zeros = () => new Array<number>(MONTHS + 1).fill(0); // index 1..MONTHS

export type StreamSeries = {
  units: number[];
  collected: number[];
  accepted: number[];
  grossDemand: number[];
  gross: number[];
  revenue: number[];
  partnerCost: number[];
  processingCost: number[];
  storageCost: number[];
  capex: number[];
};

export type ModelResult = {
  inputs: Inputs;
  streams: Record<StreamId, StreamSeries>;
  revenue: Record<LineId, number[]>;
  totalRevenue: number[];
  cogs: number[];
  grossProfit: number[];
  opex: number[];
  payroll: number[];
  ebitda: number[];
  depreciation: number[];
  ebit: number[];
  tax: number[];
  netIncome: number[];
  capex: number[];
  receipts: number[];
  netCashFlow: number[];
  cash: number[];
  catalogueHours: number[];
};

const contractRevenue = (
  t: number,
  start: number,
  perYear: number,
  growthPct: number,
  value: number,
): number => {
  if (t < start) return 0;
  const year = Math.floor((t - start) / 12);
  return ((perYear * (1 + growthPct / 100) ** year) / 12) * value;
};

export const runModel = (a: Inputs): ModelResult => {
  const params = streamParams(a);
  const take =
    (a.brokerShare / 100) * (a.brokerTake / 100) + (1 - a.brokerShare / 100) * (a.directFee / 100);
  const brokerPortion = a.brokerShare / 100;
  const window = Math.max(1, Math.round(a.licWindow));
  const life = Math.max(1, Math.round(a.life));
  const price = (base: number, t: number) => base * (1 - a.erosion / 100) ** ((t - 1) / 12);

  const streams = {} as Record<StreamId, StreamSeries>;
  for (const { id } of STREAMS) {
    const p = params[id];
    const s: StreamSeries = {
      units: zeros(),
      collected: zeros(),
      accepted: zeros(),
      grossDemand: zeros(),
      gross: zeros(),
      revenue: zeros(),
      partnerCost: zeros(),
      processingCost: zeros(),
      storageCost: zeros(),
      capex: zeros(),
    };
    let storedTb = 0;
    for (let t = 1; t <= MONTHS; t++) {
      const units = t >= p.startMonth ? Math.round(rampUnits(a, p.units, t)) : 0;
      s.units[t] = units;
      let collected = units * p.hoursPerUnit;
      if (id === "ego" && t < a.pilotStart) collected += a.valHours / Math.max(1, a.pilotStart - 1);
      const accepted = collected * p.acceptance;
      s.collected[t] = collected;
      s.accepted[t] = accepted;
      s.partnerCost[t] = (collected * p.fee * (1 + a.feeInflation / 100) ** ((t - 1) / 12)) / a.fx;
      s.processingCost[t] = accepted * p.processing;
      storedTb += (accepted * p.gb) / 1000;
      s.storageCost[t] = storedTb * a.tbCost;
      const owned = t >= p.equipmentStart ? units : 0;
      const ownedBefore = t - 1 >= p.equipmentStart ? (s.units[t - 1] ?? 0) : 0;
      s.capex[t] = (Math.max(0, owned - ownedBefore) + ownedBefore / life) * p.equipment;
    }
    // Each accepted cohort sells p.licences licences spread evenly over the
    // window, starting the month after acceptance (never before first sale).
    for (let m = 1; m <= MONTHS; m++) {
      const cohort = s.accepted[m] ?? 0;
      if (cohort === 0) continue;
      const begin = Math.max(m + 1, a.firstSale);
      for (let t = begin; t < begin + window && t <= MONTHS; t++) {
        s.grossDemand[t] =
          (s.grossDemand[t] ?? 0) + ((cohort * p.licences) / window) * price(p.price, t);
      }
    }
    for (let t = 1; t <= MONTHS; t++) {
      s.gross[t] = Math.min(s.grossDemand[t] ?? 0, p.cap / 12);
      s.revenue[t] = (s.gross[t] ?? 0) * (1 - take);
    }
    streams[id] = s;
  }

  const revenue = {
    ego: streams.ego.revenue,
    car: streams.car.revenue,
    drone: streams.drone.revenue,
    custom: zeros(),
    truth: zeros(),
  } satisfies Record<LineId, number[]>;
  const totalRevenue = zeros();
  const cogs = zeros();
  const grossProfit = zeros();
  const opex = zeros();
  const payroll = zeros();
  const ebitda = zeros();
  const depreciation = zeros();
  const ebit = zeros();
  const tax = zeros();
  const netIncome = zeros();
  const capex = zeros();
  const receipts = zeros();
  const netCashFlow = zeros();
  const cash = zeros();
  const catalogueHours = zeros();

  const lagged = (series: number[], t: number, lag: number) =>
    t - lag >= 1 ? (series[t - lag] ?? 0) : 0;
  const catalogueRevenue = zeros();
  let lossCarried = 0;
  let balance = a.openingCash;
  let hours = 0;
  cash[0] = balance;

  for (let t = 1; t <= MONTHS; t++) {
    const custom = contractRevenue(t, a.cStart, a.cPerYear, a.cGrowth, a.cValue);
    const truth = contractRevenue(t, a.gStart, a.gPerYear, a.gGrowth, a.gValue);
    revenue.custom[t] = custom;
    revenue.truth[t] = truth;
    catalogueRevenue[t] =
      streams.ego.revenue[t]! + streams.car.revenue[t]! + streams.drone.revenue[t]!;
    totalRevenue[t] = catalogueRevenue[t]! + custom + truth;

    let streamCogs = 0;
    let streamCapex = 0;
    for (const { id } of STREAMS) {
      const s = streams[id];
      streamCogs += s.partnerCost[t]! + s.processingCost[t]! + s.storageCost[t]!;
      streamCapex += s.capex[t]!;
      hours += s.accepted[t]!;
    }
    catalogueHours[t] = hours;
    cogs[t] = streamCogs + custom * (1 - a.cMargin / 100) + truth * (1 - a.gMargin / 100);
    grossProfit[t] = totalRevenue[t]! - cogs[t]!;

    const loadFactor = 1 + a.load / 100;
    payroll[t] =
      ((t >= a.opsStart ? a.opsSalary : 0) +
        (t >= a.salesStart ? a.salesSalary : 0) +
        (t >= a.founderStart ? a.founderSalary : 0)) *
      loadFactor;
    const contractsSigned =
      (t >= a.cStart
        ? (a.cPerYear * (1 + a.cGrowth / 100) ** Math.floor((t - a.cStart) / 12)) / 12
        : 0) +
      (t >= a.gStart
        ? (a.gPerYear * (1 + a.gGrowth / 100) ** Math.floor((t - a.gStart) / 12)) / 12
        : 0);
    const operating = t >= a.pilotStart;
    opex[t] =
      payroll[t]! +
      a.legal +
      a.software +
      (operating ? a.insurance + a.travel / 12 : 0) +
      contractsSigned * a.perContractLegal;
    ebitda[t] = grossProfit[t]! - opex[t]!;

    capex[t] = streamCapex;
    let dep = 0;
    for (let m = Math.max(1, t - life + 1); m <= t; m++) dep += capex[m]! / life;
    depreciation[t] = dep;
    ebit[t] = ebitda[t]! - dep;

    if (ebit[t]! < 0) {
      lossCarried += -ebit[t]!;
      tax[t] = 0;
    } else {
      const used = Math.min(lossCarried, ebit[t]!);
      lossCarried -= used;
      tax[t] = (ebit[t]! - used) * (a.taxRate / 100);
    }
    netIncome[t] = ebit[t]! - tax[t]!;

    receipts[t] =
      lagged(catalogueRevenue, t, a.brokerLag) * brokerPortion +
      lagged(catalogueRevenue, t, a.directLag) * (1 - brokerPortion) +
      lagged(revenue.custom, t, a.contractLag) +
      lagged(revenue.truth, t, a.contractLag);
    netCashFlow[t] = receipts[t]! - cogs[t]! - opex[t]! - capex[t]! - tax[t]!;
    balance += netCashFlow[t]!;
    cash[t] = balance;
  }

  return {
    inputs: a,
    streams,
    revenue,
    totalRevenue,
    cogs,
    grossProfit,
    opex,
    payroll,
    ebitda,
    depreciation,
    ebit,
    tax,
    netIncome,
    capex,
    receipts,
    netCashFlow,
    cash,
    catalogueHours,
  };
};

export const yearSum = (series: readonly number[], year: number): number => {
  let total = 0;
  for (let t = (year - 1) * 12 + 1; t <= year * 12; t++) total += series[t] ?? 0;
  return total;
};

export const monthLabel = (t: number): string => {
  const index = FIRST_MONTH.month + t - 1;
  const year = FIRST_MONTH.year + Math.floor(index / 12);
  const month = index % 12;
  return `${["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"][month]} ${year}`;
};

export const yearLabel = (year: number): string =>
  `Y${year} · ${monthLabel((year - 1) * 12 + 1)} to ${monthLabel(year * 12)}`;

export type Summary = {
  revenue5y: number;
  revenueByYear: number[];
  ebitdaByYear: number[];
  y5EbitdaMargin: number;
  capitalRequired: number;
  troughMonth: number;
  ebitdaPositiveMonth: number | null;
  cashPositiveMonth: number | null;
  catalogueHours: number;
  cumulativeEbitda: number;
  demandLimited: StreamId[];
};

export const summarize = (r: ModelResult): Summary => {
  const revenueByYear = [1, 2, 3, 4, 5].map((y) => yearSum(r.totalRevenue, y));
  const ebitdaByYear = [1, 2, 3, 4, 5].map((y) => yearSum(r.ebitda, y));
  let trough = r.cash[0]!;
  let troughMonth = 0;
  for (let t = 1; t <= MONTHS; t++) {
    if (r.cash[t]! < trough) {
      trough = r.cash[t]!;
      troughMonth = t;
    }
  }
  let ebitdaPositiveMonth: number | null = null;
  for (let t = MONTHS; t >= 1; t--) {
    if (r.ebitda[t]! > 0) ebitdaPositiveMonth = t;
    else break;
  }
  let cashPositiveMonth: number | null = null;
  for (let t = Math.max(1, troughMonth); t <= MONTHS; t++) {
    if (r.cash[t]! >= r.inputs.openingCash && t > troughMonth) {
      cashPositiveMonth = t;
      break;
    }
  }
  const demandLimited = STREAMS.filter(({ id }) => {
    const s = r.streams[id];
    return s.grossDemand.some((d, t) => t >= 1 && d > (s.gross[t] ?? 0) + 1e-6);
  }).map(({ id }) => id);
  const y5Revenue = revenueByYear[4]!;
  return {
    revenue5y: revenueByYear.reduce((x, y) => x + y, 0),
    revenueByYear,
    ebitdaByYear,
    y5EbitdaMargin: y5Revenue > 0 ? ebitdaByYear[4]! / y5Revenue : 0,
    capitalRequired: Math.max(0, r.inputs.openingCash - trough),
    troughMonth,
    ebitdaPositiveMonth,
    cashPositiveMonth,
    catalogueHours: r.catalogueHours[MONTHS]!,
    cumulativeEbitda: ebitdaByYear.reduce((x, y) => x + y, 0),
    demandLimited,
  };
};

export type UnitEconomics = {
  stream: StreamId;
  costPerAcceptedHour: number;
  netRevenuePerAcceptedHour: number;
  contributionPerAcceptedHour: number;
  contributionMargin: number;
};

// Year-one unit economics, ignoring the demand ceiling.
export const unitEconomics = (a: Inputs): UnitEconomics[] => {
  const params = streamParams(a);
  const take =
    (a.brokerShare / 100) * (a.brokerTake / 100) + (1 - a.brokerShare / 100) * (a.directFee / 100);
  const window = Math.max(1, Math.round(a.licWindow));
  return STREAMS.map(({ id }) => {
    const p = params[id];
    const cost = p.fee / a.fx / p.acceptance + p.processing + (p.gb / 1000) * a.tbCost * window;
    let net = 0;
    for (let j = 1; j <= window; j++)
      net += (p.licences / window) * p.price * (1 - a.erosion / 100) ** (j / 12) * (1 - take);
    return {
      stream: id,
      costPerAcceptedHour: cost,
      netRevenuePerAcceptedHour: net,
      contributionPerAcceptedHour: net - cost,
      contributionMargin: net > 0 ? (net - cost) / net : -1,
    };
  });
};

export const SENSITIVITY_KEYS = [
  "egoPrice",
  "egoLic",
  "erosion",
  "brokerTake",
  "egoFee",
  "egoAcc",
  "cValue",
  "cPerYear",
  "cGrowth",
  "gValue",
  "egoE",
  "egoCap",
  "carPrice",
  "dronePrice",
  "salesSalary",
] as const satisfies readonly AssumptionKey[];

export type SensitivityRow = {
  key: AssumptionKey;
  label: string;
  low: number;
  high: number;
  lowValue: number;
  highValue: number;
};

// Each driver moved 25% down and up; metric is five-year cumulative EBITDA.
export const sensitivity = (a: Inputs): { baseline: number; rows: SensitivityRow[] } => {
  const baseline = summarize(runModel(a)).cumulativeEbitda;
  const rows = SENSITIVITY_KEYS.map((key) => {
    const meta = ASSUMPTIONS.find((d) => d.key === key)!;
    const clamp = (v: number) => Math.min(meta.max, Math.max(meta.min, v));
    const lowValue = clamp(a[key] * 0.75);
    const highValue = clamp(a[key] * 1.25);
    const low = summarize(runModel({ ...a, [key]: lowValue })).cumulativeEbitda;
    const high = summarize(runModel({ ...a, [key]: highValue })).cumulativeEbitda;
    return { key, label: meta.label, low, high, lowValue, highValue };
  });
  rows.sort((x, y) => Math.abs(y.high - y.low) - Math.abs(x.high - x.low));
  return { baseline, rows };
};
