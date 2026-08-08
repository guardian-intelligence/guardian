import { useSyncExternalStore } from "react";

export type CompanyExperienceMode = "animated" | "pending" | "static";

export type StaticExperienceReason = "reduced-motion" | "save-data";

export const COMPANY_EXPERIENCE_EVENT = "guardian:company-experience";

export const COMPANY_EXPERIENCE_CRITICAL_CSS = `
html[data-company-home] body{background:#05060f}
html[data-company-experience="pending"] .company-home-beacon,
html[data-company-experience="pending"] .company-home-hero{visibility:hidden}
`;

export const COMPANY_EXPERIENCE_BOOTSTRAP = `(()=>{const r=document.documentElement;if(r.dataset.companyExperienceInitialized==="true")return;r.dataset.companyExperienceInitialized="true";const m=matchMedia("(prefers-reduced-motion: reduce)").matches,c=navigator.connection,s=Boolean(c&&c.saveData)||/(^|-)2g$/.test(c&&c.effectiveType||"");r.dataset.companyExperience=m||s?"static":"pending";r.dataset.companyExperienceReason=m?"reduced-motion":s?"save-data":"eligible"})()`;

interface CompanyExperiencePreference {
  readonly mode: "pending" | "static";
  readonly reason: "eligible" | StaticExperienceReason;
}

export function selectCompanyExperience({
  effectiveType,
  reducedMotion,
  saveData,
}: {
  readonly effectiveType?: string | undefined;
  readonly reducedMotion: boolean;
  readonly saveData: boolean;
}): CompanyExperiencePreference {
  if (reducedMotion) return { mode: "static", reason: "reduced-motion" };
  if (saveData || /(^|-)2g$/.test(effectiveType ?? "")) {
    return { mode: "static", reason: "save-data" };
  }
  return { mode: "pending", reason: "eligible" };
}

export function companyExperienceMode(): CompanyExperienceMode {
  if (typeof document === "undefined") return "static";
  const mode = document.documentElement.dataset.companyExperience;
  return mode === "animated" || mode === "pending" ? mode : "static";
}

export function prepareCompanyExperience() {
  if (typeof window === "undefined") return;
  const root = document.documentElement;
  if (root.dataset.companyExperienceInitialized === "true") return;
  root.dataset.companyExperienceInitialized = "true";
  refreshCompanyExperiencePolicy();
}

export function refreshCompanyExperiencePolicy() {
  if (typeof window === "undefined") return;
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const connection = (
    navigator as Navigator & { connection?: { effectiveType?: string; saveData?: boolean } }
  ).connection;
  const preference = selectCompanyExperience({
    effectiveType: connection?.effectiveType,
    reducedMotion,
    saveData: Boolean(connection?.saveData),
  });
  setCompanyExperience(preference.mode, preference.reason);
}

export function setCompanyExperience(mode: CompanyExperienceMode, reason?: string) {
  const root = document.documentElement;
  const unchanged =
    root.dataset.companyExperience === mode &&
    (reason === undefined || root.dataset.companyExperienceReason === reason);
  root.dataset.companyExperience = mode;
  if (reason) root.dataset.companyExperienceReason = reason;
  if (unchanged) return;
  window.dispatchEvent(
    new CustomEvent(COMPANY_EXPERIENCE_EVENT, { detail: { mode, reason: reason ?? null } }),
  );
}

function subscribeCompanyExperience(listener: () => void) {
  window.addEventListener(COMPANY_EXPERIENCE_EVENT, listener);
  return () => window.removeEventListener(COMPANY_EXPERIENCE_EVENT, listener);
}

export function useCompanyExperienceMode() {
  return useSyncExternalStore(
    subscribeCompanyExperience,
    companyExperienceMode,
    companyExperienceMode,
  );
}
