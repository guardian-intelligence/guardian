import { useFlag } from "@openfeature/react-sdk";
import { useEffect } from "react";

// Lane smoke test: mirrors the wum-flags-canary boolean onto the root
// element, where a headless session (or a curious operator) can watch the
// whole path — git → ConfigMap → OFREP re-evaluation → SSE fan-out —
// without any user-visible surface. Renders nothing.
export function FlagsCanary() {
  const { value } = useFlag("wum-flags-canary", false);
  useEffect(() => {
    document.documentElement.dataset.flagsCanary = String(value);
  }, [value]);
  return null;
}
