// The pipeline from raw real-world data to innovation, by stage, with the
// latest reported valuation (or market cap) of each member in $B as of
// October 2026. Several are talks-stage figures; see the notes. Members
// without a public valuation carry a logo but add nothing to the total.

export type CategoryId = "brokers" | "platforms" | "rl" | "labs" | "applied";

export type Member = {
  readonly name: string;
  readonly valueB: number | null;
  readonly logo?: string;
};

export type Category = {
  readonly id: CategoryId;
  readonly name: string;
  // What the stage does to the data on its way to innovation.
  readonly role: string;
  readonly members: readonly Member[];
};

// In the order data passes through them.
export const ECOSYSTEM = [
  {
    id: "brokers",
    name: "Data brokers",
    role: "License and label it",
    members: [
      { name: "Scale AI", valueB: 29, logo: "/deck/logos/scale.png" }, // Meta's June 2025 stake.
      { name: "Surge AI", valueB: 25, logo: "/deck/logos/surge.png" }, // Talks (Bloomberg).
      { name: "Mercor", valueB: 10, logo: "/deck/logos/mercor.png" },
      { name: "micro1", valueB: 4, logo: "/deck/logos/micro1.png" },
      { name: "Snorkel AI", valueB: 3.5, logo: "/deck/logos/snorkel.png" },
      { name: "Turing", valueB: 2.2 },
      { name: "Handshake AI", valueB: null, logo: "/deck/logos/handshake.png" },
      { name: "Protege", valueB: null, logo: "/deck/logos/protege.png" },
      { name: "Troveo", valueB: null, logo: "/deck/logos/troveo.png" },
      { name: "Wirestock", valueB: null, logo: "/deck/logos/wirestock.png" },
      { name: "Kled", valueB: null, logo: "/deck/logos/kled.png" },
    ],
  },
  {
    id: "platforms",
    name: "Data platforms",
    role: "Store and curate it",
    members: [
      { name: "Databricks", valueB: 190, logo: "/deck/logos/databricks.svg" },
      { name: "Snowflake", valueB: 121, logo: "/deck/logos/snowflake.svg" }, // Market cap, Oct 8 2026.
    ],
  },
  {
    id: "rl",
    name: "RL environments",
    role: "Make training tasks",
    members: [
      { name: "Prime Intellect", valueB: 1, logo: "/deck/logos/primeintellect.png" },
      { name: "Mechanize", valueB: 0.5, logo: "/deck/logos/mechanize.png" },
      { name: "Applied Compute", valueB: 3, logo: "/deck/logos/appliedcompute.png" }, // Talks.
      { name: "AfterQuery", valueB: 3.2 }, // Raising, per Forbes.
      { name: "Proximal", valueB: 0.3 },
    ],
  },
  {
    id: "labs",
    name: "Labs & neolabs",
    role: "Train the models",
    members: [
      { name: "Anthropic", valueB: 965, logo: "/deck/logos/anthropic.svg" },
      { name: "OpenAI", valueB: 852, logo: "/deck/logos/openai.svg" }, // Last close; talks at $1.4T.
      { name: "Google DeepMind", valueB: null, logo: "/deck/logos/deepmind.png" }, // Inside Alphabet.
      { name: "xAI", valueB: 250, logo: "/deck/logos/xai.png" }, // Inside SpaceX; estimate.
      { name: "Safe Superintelligence", valueB: 32 },
      { name: "Reflection AI", valueB: 25 }, // Negotiating.
      { name: "Mistral AI", valueB: 14 },
      { name: "Thinking Machines Lab", valueB: 12 },
      { name: "Periodic Labs", valueB: 7 },
    ],
  },
  {
    id: "applied",
    name: "Applied AI",
    role: "Medicine, robots, software",
    members: [
      { name: "Physical Intelligence", valueB: 5.6, logo: "/deck/logos/physicalintelligence.png" },
      { name: "Skild AI", valueB: 14, logo: "/deck/logos/skild.svg" },
      { name: "Figure", valueB: 39, logo: "/deck/logos/figure.png" },
      { name: "Isomorphic Labs", valueB: 40, logo: "/deck/logos/isomorphic.png" }, // Talks at $40B+.
      { name: "Cursor", valueB: 60, logo: "/deck/logos/cursor.png" }, // SpaceX acquisition.
    ],
  },
] as const satisfies readonly Category[];

export const categoryValue = (c: Category) =>
  c.members.reduce((sum, m) => sum + (m.valueB ?? 0), 0) * 1e9;
