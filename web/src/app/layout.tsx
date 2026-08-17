import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "NTM - Named Tmux Manager",
  description: "AI agent orchestration through tmux session management",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className="antialiased">
        {children}
        <footer
          data-ntm-version={process.env.NEXT_PUBLIC_APP_VERSION}
          className="px-4 py-3 text-center text-xs text-gray-400 dark:text-gray-600"
        >
          NTM v{process.env.NEXT_PUBLIC_APP_VERSION}
        </footer>
      </body>
    </html>
  );
}
