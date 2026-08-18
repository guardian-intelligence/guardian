import {
  ArrowDown,
  ArrowRight,
  CreditCard,
  Megaphone,
  Puzzle,
  Settings,
  ShieldCheck,
  type LucideIcon,
} from "lucide-react";

// Guardian bootstrap infographic — one transformation, left to right:
//
//   [ Latitude bare metal ] --repo bootstrap-->  [ a complete software company ]
//
// Left: the raw input — Latitude's bare metal, captioned "Bare Metal". Middle:
// the command, the single bounded Flare accent. Right: the output — five
// labelled business functions stacked on a labelled Cozystack foundation. The
// flat metal slab vs. the tall, layered company is the message: repo-declared
// bootstrap turns rented hardware into a running business.
//
// Restraint: monochrome ink on Argent, Flare reserved for the command. The
// whole graphic carries one screen-reader label (role="img").

const FUNCTIONS: ReadonlyArray<{ readonly Icon: LucideIcon; readonly label: string }> = [
  { Icon: CreditCard, label: "Billing" },
  { Icon: Megaphone, label: "GTM" },
  { Icon: Puzzle, label: "Integrations" },
  { Icon: Settings, label: "Operations" },
  { Icon: ShieldCheck, label: "Compliance" },
];

const ARIA_LABEL =
  "Running repo-declared bootstrap on Latitude bare metal produces a complete software company — billing, go-to-market, integrations, operations, and compliance — on a Cozystack foundation.";

export function GuardianUpInfographic() {
  return (
    <figure role="img" aria-label={ARIA_LABEL} data-guardian-up-infographic className="m-0 w-full">
      <div
        className="flex flex-col items-center justify-center gap-5 rounded-sm p-6 md:flex-row md:gap-8 md:p-8"
        style={{
          border: "1px solid var(--treatment-hairline)",
          background: "var(--treatment-ground)",
        }}
        aria-hidden
      >
        {/* Input — Latitude bare metal. */}
        <div className="flex shrink-0 flex-col items-center gap-3">
          <div
            className="flex items-center justify-center"
            style={{
              width: "152px",
              height: "66px",
              borderRadius: "4px",
              background: "var(--treatment-ink)",
              padding: "0 19px",
            }}
          >
            <img
              src="/brand-kit/latitude-logo.svg"
              alt="Latitude"
              style={{ width: "100%", height: "auto", display: "block" }}
            />
          </div>
          <span
            className="font-mono text-[11px] font-semibold uppercase"
            style={{ letterSpacing: "0.16em", color: "var(--treatment-ink)" }}
          >
            Bare Metal
          </span>
        </div>

        {/* Transform — the command (the one Flare moment) over an arrow. */}
        <div className="flex shrink-0 flex-col items-center gap-2">
          <span
            className="font-mono text-[13px] font-semibold"
            style={{
              background: "var(--color-flare)",
              color: "var(--color-ink)",
              padding: "5px 11px",
              borderRadius: "4px",
              whiteSpace: "nowrap",
            }}
          >
            repo&nbsp;bootstrap
          </span>
          <ArrowRight
            className="hidden md:block"
            size={30}
            strokeWidth={1.5}
            style={{ color: "var(--treatment-ink)" }}
          />
          <ArrowDown
            className="md:hidden"
            size={26}
            strokeWidth={1.5}
            style={{ color: "var(--treatment-ink)" }}
          />
        </div>

        {/* Output — a complete software company. */}
        <div
          className="w-full max-w-[440px] overflow-hidden rounded-sm"
          style={{ border: "1px solid var(--treatment-ink)" }}
        >
          {/* Upper layer — five labelled business functions. */}
          <div className="grid grid-cols-5">
            {FUNCTIONS.map(({ Icon, label }, index) => (
              <div
                key={label}
                className="flex flex-col items-center gap-1.5 px-1 py-3"
                style={{
                  background: "var(--treatment-ground)",
                  borderRight:
                    index < FUNCTIONS.length - 1 ? "1px solid var(--treatment-hairline)" : "none",
                }}
              >
                <Icon size={20} strokeWidth={1.6} style={{ color: "var(--treatment-ink)" }} />
                <span
                  className="text-center font-mono text-[9px] font-semibold uppercase leading-tight"
                  style={{ letterSpacing: "0.03em", color: "var(--treatment-muted-strong)" }}
                >
                  {label}
                </span>
              </div>
            ))}
          </div>
          {/* Lower layer — the Cozystack foundation, labelled. */}
          <div
            className="flex items-center justify-center"
            style={{
              height: "36px",
              background: "var(--treatment-ink)",
              backgroundImage: "radial-gradient(rgba(255,255,255,0.16) 1px, transparent 1px)",
              backgroundSize: "10px 10px",
              backgroundPosition: "center",
            }}
          >
            <span
              className="font-mono text-[11px] font-semibold"
              style={{ letterSpacing: "0.14em", color: "var(--color-argent)" }}
            >
              Cozystack
            </span>
          </div>
          {/* Base layer — Latitude bare metal, beneath the platform. */}
          <div
            className="flex items-center justify-center"
            style={{
              height: "34px",
              background: "var(--treatment-ink)",
              borderTop: "1px solid rgba(255, 255, 255, 0.2)",
            }}
          >
            <img
              src="/brand-kit/latitude-logo.svg"
              alt="Latitude"
              style={{ height: "15px", width: "auto", display: "block" }}
            />
          </div>
        </div>
      </div>
    </figure>
  );
}
