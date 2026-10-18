import { useEffect, useState } from "react";

// An object URL owned by the effect that revokes it. Minting it in useMemo
// instead leaves a revoked URL behind after a StrictMode remount (the
// simulated unmount revokes, the memo keeps the dead string), which breaks
// both media playback and downloads.
export function useObjectUrl(blob: Blob | null): string | null {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    if (blob === null) {
      setUrl(null);
      return undefined;
    }
    const next = URL.createObjectURL(blob);
    setUrl(next);
    return () => URL.revokeObjectURL(next);
  }, [blob]);
  return url;
}
