import type { Metadata } from "next";
import "@fontsource-variable/inter";
import "./globals.css";

export const metadata: Metadata = {
  title: { default: "1x.n back office", template: "%s · 1x.n" },
  description: "Run your restaurant: live floor, menu, tables, devices, bills and books.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en-IN" className="h-full antialiased">
      <body className="min-h-full">{children}</body>
    </html>
  );
}
