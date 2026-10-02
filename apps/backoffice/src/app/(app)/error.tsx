"use client";

import { ErrorPanel } from "@/components/error-panel";

// Errors inside a page: the sidebar stays, the content shows this.
export default function Error({ error, reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return <ErrorPanel digest={error.digest} reset={reset} />;
}
