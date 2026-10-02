"use client";

import { useRouter } from "next/navigation";
import { startTransition, useEffect, useState } from "react";

/**
 * What every error boundary shows. It keeps retrying on its own, so a wall
 * display on the live floor comes back after the API restarts or the Wi-Fi
 * drops, without anyone touching it.
 */
export function ErrorPanel({ digest, reset, every = 10 }: { digest?: string; reset: () => void; every?: number }) {
  const router = useRouter();
  const [left, setLeft] = useState(every);
  useEffect(() => {
    const id = setInterval(() => {
      setLeft((s) => {
        if (s > 1) return s - 1;
        startTransition(() => {
          router.refresh(); // re-fetch the server data...
          reset(); // ...and re-render what failed
        });
        return every;
      });
    }, 1000);
    return () => clearInterval(id);
  }, [router, reset, every]);

  return (
    <div className="mx-auto mt-10 max-w-lg rounded-xl border border-chilli-700/20 bg-chilli-50 p-6">
      <h2 className="font-semibold text-chilli-700">This page could not load</h2>
      <p className="mt-1 text-sm text-ink-soft">
        Usually the API is not running or not reachable. If you run it yourself, start it with{" "}
        <code className="rounded bg-white px-1">make run</code>.
      </p>
      <p className="mt-3 text-sm text-ink-soft" aria-live="polite">
        Trying again in {left} s.
      </p>
      {digest && <p className="mt-2 font-mono text-xs text-ink-faint">ref {digest}</p>}
      <div className="mt-4 flex gap-3">
        <button
          className="rounded-lg bg-white px-3.5 py-2 text-sm font-medium ring-1 ring-inset ring-line hover:bg-chalk"
          onClick={() =>
            startTransition(() => {
              router.refresh();
              reset();
            })
          }
        >
          Try again now
        </button>
        <a href="/sign-in" className="px-1 py-2 text-sm font-medium text-ink-soft hover:text-neel-700">
          Sign in again
        </a>
      </div>
    </div>
  );
}
