"use client";

/**
 * Navigation Bar
 *
 * Provides main navigation and connection status indicator.
 */

import Link from "next/link";
import { usePathname } from "next/navigation";
import type { ConnectionState } from "@/lib/ws/client";
import { clearConnectionConfig, resetClient } from "@/lib/api/client";
import { resetWsClient } from "@/lib/ws/client";
import { resetQueryClient } from "@/lib/hooks/use-query";

interface NavBarProps {
  wsState: ConnectionState;
}

const navItems = [
  { href: "/", label: "Sessions" },
  { href: "/agents", label: "Agents" },
  { href: "/beads", label: "Beads" },
  { href: "/mail", label: "Mail" },
  { href: "/memory", label: "Memory" },
];

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

  const handleDisconnect = () => {
    clearConnectionConfig();
    resetClient();
    resetWsClient();
    resetQueryClient();
    window.location.href = "/connect";
  };

  return (
    <header className="bg-white dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700">
      <div className="px-4 sm:px-6 lg:px-8">
        <div className="flex h-14 items-center justify-between">
          {/* Logo and nav */}
          <div className="flex items-center gap-8">
            <Link
              href="/"
              className="text-xl font-semibold text-gray-900 dark:text-white"
            >
              NTM
            </Link>
            <nav className="hidden md:flex items-center gap-1">
              {navItems.map((item) => {
                const isActive = pathname === item.href;
                return (
                  <Link
                    key={item.href}
                    href={item.href}
                    className={`px-3 py-2 rounded-md text-sm font-medium transition-colors
                              ${
                                isActive
                                  ? "bg-gray-100 dark:bg-gray-700 text-gray-900 dark:text-white"
                                  : "text-gray-600 dark:text-gray-400 hover:bg-gray-50 dark:hover:bg-gray-700/50"
                              }`}
                  >
                    {item.label}
                  </Link>
                );
              })}
            </nav>
          </div>

          {/* Status and actions */}
          <div className="flex items-center gap-4">
            <ConnectionIndicator state={wsState} />
            <button
              onClick={handleDisconnect}
              className="text-sm text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200"
            >
              Disconnect
            </button>
          </div>
        </div>
      </div>

      {/* Mobile nav */}
      <div className="md:hidden border-t border-gray-200 dark:border-gray-700 px-4 py-2">
        <nav className="flex items-center gap-1 overflow-x-auto">
          {navItems.map((item) => {
            const isActive = pathname === item.href;
            return (
              <Link
                key={item.href}
                href={item.href}
                className={`px-3 py-1.5 rounded-md text-sm font-medium whitespace-nowrap
                          ${
                            isActive
                              ? "bg-gray-100 dark:bg-gray-700 text-gray-900 dark:text-white"
                              : "text-gray-600 dark:text-gray-400"
                          }`}
              >
                {item.label}
              </Link>
            );
          })}
        </nav>
      </div>
    </header>
  );
}
