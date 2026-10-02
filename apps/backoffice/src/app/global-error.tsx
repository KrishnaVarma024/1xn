"use client";

// Last resort: the root layout itself failed, so this must bring its own
// <html> and <body>, and cannot rely on our CSS.
export default function GlobalError({ reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <html lang="en-IN">
      <body style={{ fontFamily: "system-ui, sans-serif", padding: 32, color: "#1c1e26", background: "#faf8f4" }}>
        <h2>1x.n could not load</h2>
        <p>Something failed before the page could start. Try again in a moment.</p>
        <button onClick={() => reset()} style={{ padding: "8px 14px", borderRadius: 8 }}>
          Try again
        </button>
      </body>
    </html>
  );
}
