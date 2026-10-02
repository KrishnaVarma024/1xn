import { NextResponse, type NextRequest } from "next/server";

// An optimistic check only: no token cookie means "not signed in", so go to
// /sign-in without rendering anything. Whether the token is VALID is decided
// by the Go API on every call (see src/lib/api.ts), never here.
const PUBLIC = ["/sign-in", "/sign-up"];

export function proxy(req: NextRequest) {
  const signedIn = req.cookies.has("onx_owner");
  const isPublic = PUBLIC.some((p) => req.nextUrl.pathname.startsWith(p));
  if (!signedIn && !isPublic) {
    return NextResponse.redirect(new URL("/sign-in", req.url));
  }
  return NextResponse.next();
}

export const config = {
  // Everything except Next's own files and the favicon.
  matcher: ["/((?!_next/|favicon.ico).*)"],
};
