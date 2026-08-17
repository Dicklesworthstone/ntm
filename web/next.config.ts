import type { NextConfig } from "next";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Version comes from web/package.json, which the version-lockstep test
// (internal/webui) keeps equal to the repo VERSION file. It is baked into
// the static export at build time (footer stamp in src/app/layout.tsx).
const pkg = JSON.parse(
  readFileSync(join(process.cwd(), "package.json"), "utf8")
) as { version?: string };

const nextConfig: NextConfig = {
  // Static export: `next build` emits a fully static site into out/.
  // Next/React are BUILD-TIME dependencies only — no Node server ships
  // (F1 maintenance contract, bd-ws5-ship-or-cut-jv0rc.1).
  output: "export",

  // Directory-style URLs (route/index.html) so the embedded Go file server
  // can serve each route at its clean path.
  trailingSlash: true,

  // The image optimizer needs a server; the export must not.
  images: { unoptimized: true },

  // Enable React strict mode for development
  reactStrictMode: true,

  // Environment variables exposed to the browser
  env: {
    NEXT_PUBLIC_APP_VERSION: pkg.version || "0.0.0",
  },
};

export default nextConfig;
