export default function AuthLayout({ children }: LayoutProps<"/">) {
  return (
    <main className="flex min-h-screen items-start justify-center px-4 py-12 sm:py-20">
      <div className="w-full max-w-lg">
        <div className="mb-8 flex items-center gap-2">
          <span className="grid h-9 w-9 place-items-center rounded-lg bg-neel-700 text-sm font-bold text-white">1x</span>
          <span className="text-lg font-semibold">1x.n back office</span>
        </div>
        {children}
      </div>
    </main>
  );
}
