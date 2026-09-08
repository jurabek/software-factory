import type { SVGProps } from "react";

// Compact line icons tuned for the monospace UI. 16px grid, currentColor stroke.
function Base({ children, ...props }: SVGProps<SVGSVGElement> & { children: React.ReactNode }) {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...props}>
      {children}
    </svg>
  );
}

export const IconPencil = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M11.5 2.5l2 2L6 12l-2.5.5.5-2.5 7.5-7.5z" /></Base>;
export const IconCopy = (props: SVGProps<SVGSVGElement>) => <Base {...props}><rect x="5.5" y="5.5" width="8" height="8" rx="1" /><path d="M10.5 5.5V3.5a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2" /></Base>;
export const IconFolder = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M2 4.5A1 1 0 0 1 3 3.5h3l1.5 1.5H13a1 1 0 0 1 1 1V12a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V4.5z" /></Base>;
export const IconTerminal = (props: SVGProps<SVGSVGElement>) => <Base {...props}><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M5 6.5l2 1.5-2 1.5M8.5 10h3" /></Base>;
export const IconSplit = (props: SVGProps<SVGSVGElement>) => <Base {...props}><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M9.5 3v10M12 6l-2 2 2 2" /></Base>;
export const IconPlus = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M8 3v10M3 8h10" /></Base>;
export const IconShare = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M8 10V2.5M5.5 5L8 2.5 10.5 5M3 9v3a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1V9" /></Base>;
export const IconSearch = (props: SVGProps<SVGSVGElement>) => <Base {...props}><circle cx="7" cy="7" r="4" /><path d="M10 10l3.5 3.5" /></Base>;
export const IconMonitor = (props: SVGProps<SVGSVGElement>) => <Base {...props}><rect x="2" y="3" width="12" height="8" rx="1" /><path d="M6 13.5h4M8 11.5v2" /></Base>;
export const IconSend = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M14 2L7 9M14 2l-4.5 12-2.5-5-5-2.5L14 2z" /></Base>;
export const IconChevron = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M6 4l4 4-4 4" /></Base>;
export const IconShield = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M8 2l5 2v4c0 3.5-2.5 5.5-5 6.5C5.5 13.5 3 11.5 3 8V4l5-2z" /></Base>;
export const IconFastForward = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M2 4l5 4-5 4V4zM8 4l5 4-5 4V4z" /></Base>;
export const IconSkip = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M3 4l7 4-7 4V4zM12 3.5v9" /></Base>;
export const IconArchive = (props: SVGProps<SVGSVGElement>) => <Base {...props}><rect x="2.5" y="3" width="11" height="3" rx="0.5" /><path d="M3.5 6v6.5a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1V6M6.5 9h3" /></Base>;
export const IconBranch = (props: SVGProps<SVGSVGElement>) => <Base {...props}><circle cx="4.5" cy="3.5" r="1.5" /><circle cx="4.5" cy="12.5" r="1.5" /><circle cx="11.5" cy="4.5" r="1.5" /><path d="M4.5 5v6M11.5 6c0 3-3.5 2.5-3.5 5" /></Base>;
export const IconPeople = (props: SVGProps<SVGSVGElement>) => <Base {...props}><circle cx="6" cy="5.5" r="2" /><path d="M2.5 13c0-2 1.5-3.5 3.5-3.5S9.5 11 9.5 13M10.5 4a2 2 0 0 1 0 4M11 9.5c1.6.3 2.5 1.6 2.5 3.5" /></Base>;
export const IconRobot = (props: SVGProps<SVGSVGElement>) => <Base {...props}><rect x="3" y="5.5" width="10" height="7" rx="1.5" /><path d="M8 3v2.5M5.5 8.5v1M10.5 8.5v1M6 12.5v1M10 12.5v1" /><circle cx="8" cy="2.5" r="0.8" /></Base>;
export const IconFile = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M4 2h5l3 3v9a0 0 0 0 1 0 0H4a0 0 0 0 1 0 0V2z" /><path d="M9 2v3h3" /></Base>;
export const IconGear = (props: SVGProps<SVGSVGElement>) => <Base {...props}><circle cx="8" cy="8" r="2" /><path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.5 3.5l1.4 1.4M11.1 11.1l1.4 1.4M12.5 3.5l-1.4 1.4M4.9 11.1l-1.4 1.4" /></Base>;
export const IconChat = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M2.5 4a1 1 0 0 1 1-1h9a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H6l-3 2.5V11H3.5a1 1 0 0 1-1-1V4z" /></Base>;
export const IconCollapse = (props: SVGProps<SVGSVGElement>) => <Base {...props}><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M6 3v10M11 6l-2 2 2 2" /></Base>;
export const IconExpand = (props: SVGProps<SVGSVGElement>) => <Base {...props}><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M6 3v10M9 6l2 2-2 2" /></Base>;
export const IconBrain = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M6.5 2.5A2 2 0 0 0 4.5 4a1.8 1.8 0 0 0-1.2 3 2 2 0 0 0 .3 3.4A1.8 1.8 0 0 0 6.5 13V2.5z" /><path d="M9.5 2.5A2 2 0 0 1 11.5 4a1.8 1.8 0 0 1 1.2 3 2 2 0 0 1-.3 3.4A1.8 1.8 0 0 1 9.5 13V2.5z" /></Base>;
export const IconExternalLink = (props: SVGProps<SVGSVGElement>) => <Base {...props}><path d="M9 3h4v4M13 3l-6 6M11 9.5V12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h2.5" /></Base>;
