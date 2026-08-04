export type CompanyExperienceMode = "animated" | "pending" | "static";

export type StaticExperienceReason =
  | "init-timeout"
  | "reduced-motion"
  | "renderer-unavailable"
  | "save-data"
  | "title-unavailable";

export const COMPANY_EXPERIENCE_EVENT = "guardian:company-experience";
export const TITLE_MATERIALIZATION_EVENT = "guardian:title-materialization";

export const COMPANY_EXPERIENCE_BOOTSTRAP = `(()=>{const r=document.documentElement;if(r.dataset.companyExperienceInitialized==="true")return;r.dataset.companyExperienceInitialized="true";const m=matchMedia("(prefers-reduced-motion: reduce)").matches,c=navigator.connection,s=Boolean(c&&c.saveData)||/(^|-)2g$/.test(c&&c.effectiveType||"");if(m||s){r.dataset.companyExperience="static";r.dataset.companyExperienceReason=m?"reduced-motion":"save-data";return}r.dataset.companyExperience="pending";r.dataset.companyExperienceReason="eligible";window.__guardianExperienceWatchdog=setTimeout(()=>{if(r.dataset.companyExperience==="pending"){r.dataset.companyExperience="static";r.dataset.companyExperienceReason="init-timeout";dispatchEvent(new CustomEvent("${COMPANY_EXPERIENCE_EVENT}",{detail:{mode:"static",reason:"init-timeout"}}))}},900)})()`;

type ExperienceWindow = Window & {
  __guardianExperienceWatchdog?: ReturnType<typeof setTimeout>;
};

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
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  const connection = (
    navigator as Navigator & { connection?: { effectiveType?: string; saveData?: boolean } }
  ).connection;
  const saveData =
    Boolean(connection?.saveData) || /(^|-)2g$/.test(connection?.effectiveType ?? "");
  if (reducedMotion || saveData) {
    setCompanyExperience("static", reducedMotion ? "reduced-motion" : "save-data");
    return;
  }
  setCompanyExperience("pending", "eligible");
}

export function setCompanyExperience(mode: CompanyExperienceMode, reason?: string) {
  const root = document.documentElement;
  root.dataset.companyExperience = mode;
  if (reason) root.dataset.companyExperienceReason = reason;
  const experienceWindow = window as ExperienceWindow;
  if (mode !== "pending" && experienceWindow.__guardianExperienceWatchdog) {
    clearTimeout(experienceWindow.__guardianExperienceWatchdog);
    delete experienceWindow.__guardianExperienceWatchdog;
  }
  window.dispatchEvent(
    new CustomEvent(COMPANY_EXPERIENCE_EVENT, { detail: { mode, reason: reason ?? null } }),
  );
}

export function markTitleMaterialization(state: "failed" | "ready") {
  window.dispatchEvent(new CustomEvent(TITLE_MATERIALIZATION_EVENT, { detail: { state } }));
}

export function waitForTitleMaterialization() {
  const current = document.querySelector<HTMLCanvasElement>("[data-title-materialization]");
  if (current?.dataset.titleMaterialization === "ready") return Promise.resolve(true);
  if (current?.dataset.titleMaterialization === "failed") return Promise.resolve(false);
  return new Promise<boolean>((resolve) => {
    const onState = (event: Event) => {
      const state = (event as CustomEvent<{ state?: string }>).detail?.state;
      if (state !== "ready" && state !== "failed") return;
      window.removeEventListener(TITLE_MATERIALIZATION_EVENT, onState);
      resolve(state === "ready");
    };
    window.addEventListener(TITLE_MATERIALIZATION_EVENT, onState);
  });
}
