import type { Metadata } from "next";
import type { ReactNode } from "react";
import { TooltipProvider } from "@/components/ui/tooltip.tsx";
import "./globals.css";

export const metadata: Metadata = {
  title: "Software Factory",
  description: "Coordinate software work across connected daemons.",
  robots: { index: false, follow: false },
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return <html lang="en" className="dark"><body className="antialiased"><TooltipProvider delayDuration={300}>{children}</TooltipProvider></body></html>;
}
