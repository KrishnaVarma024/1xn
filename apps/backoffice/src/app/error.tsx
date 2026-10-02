"use client";

import { ErrorPanel } from "@/components/error-panel";

// Errors in the signed-in layout itself (it asks the API who you are, so it
// fails first when the API is down). (app)/error.tsx cannot catch those: a
// boundary only catches errors below its own layout.
export default function Error({ error, reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <main className="px-4">
      <ErrorPanel digest={error.digest} reset={reset} />
    </main>
  );
}
