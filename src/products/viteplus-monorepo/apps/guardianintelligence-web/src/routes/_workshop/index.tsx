import { createFileRoute } from "@tanstack/react-router";
import {
  COMPANY_EXPERIENCE_BOOTSTRAP,
  prepareCompanyExperience,
} from "~/components/company-home/company-experience";
import { canonicalLink, ogMeta } from "~/lib/head";

export const Route = createFileRoute("/_workshop/")({
  beforeLoad: () => prepareCompanyExperience(),
  component: LandingPage,
  head: () => ({
    meta: ogMeta({
      slug: "home",
      title: "Guardian Intelligence",
      description:
        "Guardian Intelligence builds reusable software in the open and private systems under enterprise contract.",
      path: "/",
      imageFormat: "png",
    }),
    links: [canonicalLink("/")],
    scripts: [{ children: COMPANY_EXPERIENCE_BOOTSTRAP }],
  }),
});

function LandingPage() {
  return null;
}
