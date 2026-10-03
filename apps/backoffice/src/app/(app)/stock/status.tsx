import { Badge } from "@/components/ui";
import type { StockItem } from "@/lib/types";

export function StockStatus({ status }: { status: StockItem["status"] }) {
  switch (status) {
    case "low":
      return <Badge tone="warn">Low</Badge>;
    case "out":
      return <Badge tone="bad">Out</Badge>;
    case "negative":
      return <Badge tone="bad">Below zero</Badge>;
    case "new":
      return <Badge>Not stocked yet</Badge>;
    default:
      return <Badge tone="good">OK</Badge>;
  }
}
