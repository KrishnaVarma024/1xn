export default function Loading() {
  return (
    <div aria-busy="true" aria-label="Loading" className="animate-pulse space-y-4">
      <div className="h-7 w-48 rounded bg-line" />
      <div className="grid gap-4 sm:grid-cols-3">
        <div className="h-20 rounded-xl bg-line/60" />
        <div className="h-20 rounded-xl bg-line/60" />
        <div className="h-20 rounded-xl bg-line/60" />
      </div>
      <div className="h-64 rounded-xl bg-line/40" />
    </div>
  );
}
