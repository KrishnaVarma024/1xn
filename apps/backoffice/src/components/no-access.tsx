import Link from "next/link";
import { EmptyState, PageHeader } from "@/components/ui";
import { getContext } from "@/lib/context";
import { personRoleLabel } from "@/lib/labels";

/** What a page shows to someone whose role cannot use it. */
export async function NoAccess({ page }: { page: string }) {
  const { me } = await getContext();
  return (
    <>
      <PageHeader title={page} />
      <EmptyState title={`${personRoleLabel[me.user.role] ?? me.user.role}s can't open this page`}>
        Ask the owner if you need it.{" "}
        <Link href="/floor" className="font-medium text-neel-700 hover:underline">
          Go to the live floor
        </Link>
      </EmptyState>
    </>
  );
}
