import os from "node:os";
import path from "node:path";
import type { NextConfig } from "next";

// In development, Next.js serves its dev-only files (hot reload, page chunks)
// only to pages opened on localhost. Opening the back office from a phone on
// the same Wi-Fi uses this computer's network address (the "Network:" line
// `next dev` prints), so those addresses are allowed too: this machine's own,
// nobody else's. Add more with ONEXN_DEV_ORIGINS="a.example,b.example".
// Production ignores this setting.
function devOrigins(): string[] {
  const own = Object.values(os.networkInterfaces())
    .flat()
    .filter((a): a is os.NetworkInterfaceInfo => !!a && a.family === "IPv4" && !a.internal)
    .map((a) => a.address);
  const extra = (process.env.ONEXN_DEV_ORIGINS ?? "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  return [...own, ...extra];
}

const nextConfig: NextConfig = {
  // This folder is the app's root. Without it, a package-lock.json further up
  // (in your home folder, say) makes Next.js guess that folder instead, warn
  // about "multiple lockfiles", and watch far more files than it needs.
  turbopack: { root: path.resolve(__dirname) },
  allowedDevOrigins: devOrigins(),
  // The back office never needs to be embedded in another site.
  async headers() {
    return [
      {
        source: "/:path*",
        headers: [
          { key: "X-Frame-Options", value: "DENY" },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "Referrer-Policy", value: "same-origin" },
        ],
      },
    ];
  },
};

export default nextConfig;
