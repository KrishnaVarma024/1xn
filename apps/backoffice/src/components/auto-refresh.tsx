"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";

/**
 * Re-fetches the page's server data every few seconds while the tab is
 * visible. router.refresh() re-renders the Server Components with fresh API
 * data and keeps scroll position and client state. Pull now, push hints
 * (WebSocket) later: the page will not change.
 */
export function AutoRefresh({ seconds = 5 }: { seconds?: number }) {
  const router = useRouter();
  const [at, setAt] = useState<string>("");
  useEffect(() => {
    const tick = () => {
      if (document.visibilityState === "visible") {
        router.refresh();
        setAt(new Date().toLocaleTimeString("en-IN", { hour: "numeric", minute: "2-digit", second: "2-digit" }));
      }
    };
    const id = setInterval(tick, seconds * 1000);
    document.addEventListener("visibilitychange", tick);
    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", tick);
    };
  }, [router, seconds]);
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-ink-faint">
      <span className="h-2 w-2 animate-pulse rounded-full bg-curry-700" aria-hidden />
      Live{at && ` · ${at}`}
    </span>
  );
}
