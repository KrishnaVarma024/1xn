import { Nav } from "@/components/nav";
import { getContext } from "@/lib/context";
import { personRoleLabel } from "@/lib/labels";
import { signOut } from "../(auth)/actions";

export default async function AppLayout({ children }: LayoutProps<"/">) {
  const { me, outlet } = await getContext();
  return (
    <div className="min-h-screen md:flex">
      <aside className="border-b border-line bg-white print:hidden md:sticky md:top-0 md:h-screen md:w-60 md:shrink-0 md:border-b-0 md:border-r">
        <div className="flex items-center justify-between gap-3 px-4 py-4 md:block">
          <div className="flex items-center gap-2">
            <span className="grid h-8 w-8 place-items-center rounded-lg bg-neel-700 text-xs font-bold text-white">1x</span>
            <div className="min-w-0">
              <div className="truncate text-sm font-semibold" data-testid="business-name">
                {me.business_name}
              </div>
              <div className="truncate text-xs text-ink-soft">{outlet.name}</div>
            </div>
          </div>
          <form action={signOut} className="md:hidden">
            <button className="text-xs font-medium text-ink-soft hover:text-chilli-700">Sign out</button>
          </form>
        </div>
        <div className="px-3 pb-3 md:pb-0">
          <Nav perms={me.permissions} />
        </div>
        <div className="hidden px-4 py-4 md:absolute md:bottom-0 md:block md:w-60">
          <div className="mb-2 truncate text-xs text-ink-faint" data-testid="signed-in-as">
            Signed in as {me.user.name} · {personRoleLabel[me.user.role] ?? me.user.role}
          </div>
          <form action={signOut}>
            <button className="text-xs font-medium text-ink-soft hover:text-chilli-700">Sign out</button>
          </form>
        </div>
      </aside>
      <main className="min-w-0 flex-1 px-4 py-6 sm:px-8 sm:py-8 print:p-0">
        <div className="mx-auto max-w-6xl">{children}</div>
      </main>
    </div>
  );
}
