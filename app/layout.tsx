import type { Metadata, Viewport } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "NERVE — Network operations",
  description: "Self-hosted network inventory, topology, events, and infrastructure management.",
  metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL ?? "http://localhost:3000"),
  openGraph: {
    title: "NERVE — Network operations",
    description: "See it. Understand it. Control it.",
    images: [{ url: "/og.png", width: 1729, height: 910, alt: "NERVE — See it. Understand it. Control it." }],
  },
  twitter: {
    card: "summary_large_image",
    title: "NERVE — Network operations",
    description: "See it. Understand it. Control it.",
    images: ["/og.png"],
  },
};

export const viewport: Viewport = {
  colorScheme: "light dark",
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#f4f6f4" },
    { media: "(prefers-color-scheme: dark)", color: "#111513" },
  ],
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body>{children}</body>
    </html>
  );
}
