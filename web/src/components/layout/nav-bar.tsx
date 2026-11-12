"use client";

/**
 * Navigation Bar
 *
 * Provides main navigation across all dashboard surfaces, responsive mobile
 * drawer navigation, and WebSocket connection status indicator.
 */

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState, type ReactNode } from "react";
import type { ConnectionState } from "@/lib/ws/client";
import {
  browserNavigation,
  clearConnectionConfig,
  resetClient,
} from "@/lib/api/client";
import { resetWsClient } from "@/lib/ws/client";
import { resetQueryClient } from "@/lib/hooks/use-query";

interface NavBarProps {
  wsState: ConnectionState;
}

export interface NavItem {
  href: string;
  label: string;
  icon: ReactNode;
}

function LucideSvg({ children }: { children: ReactNode }) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className="h-4 w-4 shrink-0"
    >
      {children}
    </svg>
  );
}

export const navItems: NavItem[] = [
  {
    href: "/",
    label: "Overview",
    icon: (
      <LucideSvg>
        <rect width="7" height="9" x="3" y="3" rx="1" />
        <rect width="7" height="5" x="14" y="3" rx="1" />
        <rect width="7" height="9" x="14" y="12" rx="1" />
        <rect width="7" height="5" x="3" y="16" rx="1" />
      </LucideSvg>
    ),
  },
  {
    href: "/sessions",
    label: "Sessions",
    icon: (
      <LucideSvg>
        <polyline points="4 17 10 11 4 5" />
        <line x1="12" x2="20" y1="19" y2="19" />
      </LucideSvg>
    ),
  },
  {
    href: "/agents",
    label: "Agents",
    icon: (
      <LucideSvg>
        <path d="M12 8V4H8" />
        <rect width="16" height="12" x="4" y="8" rx="2" />
        <path d="M2 14h2" />
        <path d="M20 14h2" />
        <path d="M15 13v2" />
        <path d="M9 13v2" />
      </LucideSvg>
    ),
  },
  {
    href: "/pipelines",
    label: "Pipelines",
    icon: (
      <LucideSvg>
        <circle cx="6" cy="6" r="3" />
        <path d="M6 9v12" />
        <circle cx="18" cy="9" r="3" />
        <path d="M18 12a6 6 0 0 1-6 6H6" />
      </LucideSvg>
    ),
  },
  {
    href: "/beads",
    label: "Beads",
    icon: (
      <LucideSvg>
        <rect width="18" height="18" x="3" y="3" rx="2" />
        <path d="M9 8v8" />
        <path d="M15 8v5" />
      </LucideSvg>
    ),
  },
  {
    href: "/mail",
    label: "Mail",
    icon: (
      <LucideSvg>
        <rect width="20" height="16" x="2" y="4" rx="2" />
        <path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7" />
      </LucideSvg>
    ),
  },
  {
    href: "/scanner",
    label: "Scanner",
    icon: (
      <LucideSvg>
        <path d="M3 7V5a2 2 0 0 1 2-2h2" />
        <path d="M17 3h2a2 2 0 0 1 2 2v2" />
        <path d="M21 17v2a2 2 0 0 1-2 2h-2" />
        <path d="M7 21H5a2 2 0 0 1-2-2v-2" />
        <circle cx="12" cy="12" r="3" />
        <path d="m16 16-1.9-1.9" />
      </LucideSvg>
    ),
  },
  {
    href: "/analytics",
    label: "Analytics",
    icon: (
      <LucideSvg>
        <path d="M3 3v18h18" />
        <path d="M18 17V9" />
        <path d="M13 17V5" />
        <path d="M8 17v-3" />
      </LucideSvg>
    ),
  },
  {
    href: "/accounts",
    label: "Accounts",
    icon: (
      <LucideSvg>
        <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
        <circle cx="9" cy="7" r="4" />
        <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
        <path d="M16 3.13a4 4 0 0 1 0 7.75" />
      </LucideSvg>
    ),
  },
  {
    href: "/safety",
    label: "Safety",
    icon: (
      <LucideSvg>
        <path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z" />
        <path d="m9 12 2 2 4-4" />
      </LucideSvg>
    ),
  },
  {
    href: "/memory",
    label: "Memory",
    icon: (
      <LucideSvg>
        <ellipse cx="12" cy="5" rx="9" ry="3" />
        <path d="M3 5V19A9 3 0 0 0 21 19V5" />
        <path d="M3 12A9 3 0 0 0 21 12" />
      </LucideSvg>
    ),
  },
];

function normalizePath(path: string | null): string {
  if (!path || path === "/") return "/";
  return path.replace(/\/+$/, "") || "/";
}

export function isNavItemActive(pathname: string | null, href: string): boolean {
  const current = normalizePath(pathname);
  const target = normalizePath(href);
  if (target === "/") {
    return current === "/";
  }
  return current === target || current.startsWith(`${target}/`);
}

function ConnectionIndicator({ state }: { state: ConnectionState }) {
  const colors: Record<ConnectionState, string> = {
    connected: "bg-green-500",
    connecting: "bg-yellow-500 animate-pulse",
    reconnecting: "bg-yellow-500 animate-pulse",
    disconnected: "bg-red-500",
  };

  const labels: Record<ConnectionState, string> = {
    connected: "Connected",
    connecting: "Connecting...",
    reconnecting: "Reconnecting...",
    disconnected: "Disconnected",
  };

  return (
    <div className="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-400">
      <div className={`w-2 h-2 rounded-full ${colors[state]}`} />
      <span className="hidden sm:inline">{labels[state]}</span>
    </div>
  );
}

export function NavBar({ wsState }: NavBarProps) {
  const pathname = usePathname();
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  const handleDisconnect = () => {
    clearConnectionConfig();
    resetClient();
    resetWsClient();
    resetQueryClient();
    browserNavigation.redirect("/connect");
  };

  return (
    <header className="bg-white dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700">
      <div className="px-4 sm:px-6 lg:px-8">
        <div className="flex h-14 items-center justify-between gap-4">
          {/* Logo and desktop nav */}
          <div className="flex items-center gap-6 min-w-0">
            <Link
              href="/"
              className="text-xl font-semibold text-gray-900 dark:text-white shrink-0"
            >
              NTM
            </Link>
            <nav
              aria-label="Main navigation"
              className="hidden lg:flex items-center gap-1 overflow-x-auto"
            >
              {navItems.map((item) => {
                const isActive = isNavItemActive(pathname, item.href);
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    aria-current={isActive ? "page" : undefined}
                    className={`inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm font-medium whitespace-nowrap transition-colors ${
                      isActive
                        ? "bg-gray-100 dark:bg-gray-700 text-gray-900 dark:text-white"
                        : "text-gray-600 dark:text-gray-400 hover:bg-gray-50 dark:hover:bg-gray-700/50 hover:text-gray-900 dark:hover:text-white"
                    }`}
                  >
                    {item.icon}
                    <span>{item.label}</span>
                  </Link>
                );
              })}
            </nav>
          </div>

          {/* Status, mobile drawer toggle, and disconnect */}
          <div className="flex items-center gap-3 shrink-0">
            <ConnectionIndicator state={wsState} />
            <button
              type="button"
              onClick={handleDisconnect}
              className="text-sm text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200"
            >
              Disconnect
            </button>
            <button
              type="button"
              aria-label="Toggle navigation menu"
              aria-expanded={mobileMenuOpen}
              aria-controls="mobile-navigation-drawer"
              onClick={() => setMobileMenuOpen((prev) => !prev)}
              className="lg:hidden inline-flex items-center justify-center rounded-md p-2 text-gray-600 hover:bg-gray-100 hover:text-gray-900 dark:text-gray-300 dark:hover:bg-gray-700 dark:hover:text-white"
            >
              {mobileMenuOpen ? (
                <LucideSvg>
                  <path d="M18 6 6 18" />
                  <path d="m6 6 12 12" />
                </LucideSvg>
              ) : (
                <LucideSvg>
                  <line x1="4" x2="20" y1="12" y2="12" />
                  <line x1="4" x2="20" y1="6" y2="6" />
                  <line x1="4" x2="20" y1="18" y2="18" />
                </LucideSvg>
              )}
            </button>
          </div>
        </div>
      </div>

      {/* Responsive mobile navigation drawer */}
      {mobileMenuOpen && (
        <div
          id="mobile-navigation-drawer"
          className="lg:hidden border-t border-gray-200 dark:border-gray-700 px-4 py-3 bg-white dark:bg-gray-800"
        >
          <nav
            aria-label="Mobile navigation"
            className="grid grid-cols-2 sm:grid-cols-3 gap-1.5"
          >
            {navItems.map((item) => {
              const isActive = isNavItemActive(pathname, item.href);
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  aria-current={isActive ? "page" : undefined}
                  onClick={() => setMobileMenuOpen(false)}
                  className={`inline-flex items-center gap-2 px-3 py-2 rounded-md text-sm font-medium whitespace-nowrap transition-colors ${
                    isActive
                      ? "bg-gray-100 dark:bg-gray-700 text-gray-900 dark:text-white"
                      : "text-gray-600 dark:text-gray-400 hover:bg-gray-50 dark:hover:bg-gray-700/50"
                  }`}
                >
                  {item.icon}
                  <span>{item.label}</span>
                </Link>
              );
            })}
          </nav>
        </div>
      )}
    </header>
  );
}
