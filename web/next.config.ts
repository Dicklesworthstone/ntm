import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Enable React strict mode for development
  reactStrictMode: true,

  // Experimental features for Next.js 16
  experimental: {
    // Enable server actions
    serverActions: {
      bodySizeLimit: "2mb",
    },
  },

  // Allow connections to local NTM server during development
  async rewrites() {
    // Proxy API requests to NTM server when NEXT_PUBLIC_NTM_URL is not set
    // This helps with CORS during development
    const ntmUrl = process.env.NEXT_PUBLIC_NTM_URL || "http://localhost:8080";
    return [
      {
        source: "/api/ntm/:path*",
        destination: `${ntmUrl}/api/:path*`,
      },
      {
        source: "/ws",
        destination: `${ntmUrl}/api/v1/ws`,
      },
    ];
  },

  // Environment variables exposed to the browser
  env: {
    NEXT_PUBLIC_APP_VERSION: process.env.npm_package_version || "0.1.0",
  },
};

export default nextConfig;
