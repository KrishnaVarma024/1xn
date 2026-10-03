"use client";

import { Button } from "./ui";

export function PrintButton({ children = "Print" }: { children?: string }) {
  return (
    <Button type="button" variant="secondary" onClick={() => window.print()} className="print:hidden">
      {children}
    </Button>
  );
}
