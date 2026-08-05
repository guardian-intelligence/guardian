import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { AppChrome } from "@guardian/brand";
import { TopNav } from "~/components/top-nav";

export function CompanyHomeHeader() {
  return (
    <AppChrome
      treatment="workshop"
      className="company-home-header"
      LinkComponent={LinkAdapter}
      slotRight={<TopNav />}
      bottomRule={false}
      sticky={false}
    />
  );
}

function LinkAdapter(props: {
  to: string;
  className?: string;
  style?: React.CSSProperties;
  "aria-label"?: string;
  onClick?: React.MouseEventHandler;
  children?: ReactNode;
}) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return <Link {...(props as any)} />;
}
