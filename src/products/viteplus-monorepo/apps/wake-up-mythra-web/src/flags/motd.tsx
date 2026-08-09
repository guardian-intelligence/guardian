import { useFlag } from "@openfeature/react-sdk";

// The dog park's message of the day: a string flag, empty by default. Set
// it in the flag control plane and every open page shows the banner
// mid-session; clear it and the banner leaves the same way.
export function Motd() {
  const { value } = useFlag("wum-motd", "");
  if (!value) return null;
  return <div className="motd">{value}</div>;
}
