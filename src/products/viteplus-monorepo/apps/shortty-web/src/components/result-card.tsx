import { useEffect, useMemo } from "react";
import type { EncodeResult } from "~/engine/client";
import { SIZE_LIMIT_BYTES } from "~/engine/limits";
import { formatBytes } from "~/lib/format";

export interface ResultCardProps {
  readonly result: EncodeResult;
  readonly sourceName: string;
  readonly onBack: () => void;
}

export function ResultCard({ result, sourceName, onBack }: ResultCardProps) {
  const url = useMemo(() => URL.createObjectURL(result.blob), [result.blob]);
  useEffect(() => () => URL.revokeObjectURL(url), [url]);

  const { outcome } = result;
  const downloadName = `${sourceName.replace(/\.[^.]+$/, "") || "clip"}-shortty.mp4`;

  return (
    <div className="glass-strong overflow-hidden">
      {/* Result preview plays the exact bytes the user will download. */}
      {/* eslint-disable-next-line jsx-a11y/media-has-caption */}
      <video src={url} controls playsInline className="max-h-[48vh] w-full bg-black" />
      <div className="space-y-5 p-6">
        <div className="flex flex-wrap items-center gap-3">
          <span className="rounded-full border border-glow-teal/40 bg-glow-teal/10 px-3 py-1 font-mono text-sm text-glow-teal">
            {formatBytes(outcome.bytes)} — under the limit, guaranteed
          </span>
          {outcome.mode === "remux" ? (
            <span className="rounded-full border border-glow-warm/40 bg-glow-warm/10 px-3 py-1 text-sm text-glow-warm">
              Original quality — no re-encode
            </span>
          ) : (
            <span className="rounded-full border border-line-strong px-3 py-1 text-sm text-mist-dim">
              {outcome.height}p · {Math.round(outcome.frameRate)} fps
              {outcome.passes.length > 1 ? ` · ${outcome.passes.length} passes` : ""}
            </span>
          )}
        </div>
        <UtilizationMeter utilization={outcome.utilization} />
        <div className="flex flex-wrap gap-3">
          <a
            href={url}
            download={downloadName}
            className="rounded-xl bg-mist px-6 py-3 font-medium text-night transition-transform hover:scale-[1.02]"
          >
            Download
          </a>
          <button
            type="button"
            onClick={onBack}
            className="rounded-xl border border-line-strong px-6 py-3 text-mist-dim transition-colors hover:text-mist"
          >
            Adjust selection
          </button>
        </div>
      </div>
    </div>
  );
}

function UtilizationMeter({ utilization }: { utilization: number }) {
  const pct = Math.min(utilization * 100, 100);
  return (
    <div>
      <div className="mb-1 flex justify-between font-mono text-xs text-mist-faint">
        <span>budget used</span>
        <span>
          {pct.toFixed(1)}% of {formatBytes(SIZE_LIMIT_BYTES)}
        </span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-white/[0.06]">
        <div
          className="h-full rounded-full bg-gradient-to-r from-glow-violet to-glow-teal"
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}
