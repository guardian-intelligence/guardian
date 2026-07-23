import type { TestInfo } from "@playwright/test";
import type { FoldStatus } from "./fold-probe.ts";

export type Severity = "critical" | "warning";

export interface FoldDropFinding {
  kind: "fold-drop";
  severity: Severity;
  target: string;
  formFactor: string;
  selector: string;
  status: FoldStatus;
  clippedPx: number;
}

export interface VisualDriftFinding {
  kind: "visual-drift";
  severity: Severity;
  target: string;
  formFactor: string;
  engine: string;
  message: string;
}

export type Finding = FoldDropFinding | VisualDriftFinding;

export const FINDING_ATTACHMENT = "visual-harness-finding";

/** Attach a finding so the reporter can emit it as a structured JSON line. */
export async function attachFinding(testInfo: TestInfo, finding: Finding): Promise<void> {
  await testInfo.attach(FINDING_ATTACHMENT, {
    body: JSON.stringify(finding),
    contentType: "application/json",
  });
}
