// Minimal stroke icons (Feather/Lucide style) — crisp at 16px, no dependency.
// Every glyph in the app comes from this set; we never rely on emoji, since
// color-emoji fonts vary by OS and would clash with the monochrome UI.
import { ReactNode } from "react";

const base = {
  width: 16, height: 16, viewBox: "0 0 24 24", fill: "none",
  stroke: "currentColor", strokeWidth: 2, strokeLinecap: "round" as const, strokeLinejoin: "round" as const,
};

const svg = (children: ReactNode) => (
  <svg {...base}>{children}</svg>
);

const gearPath = <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" /></>;
const shieldPath = <><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" /></>;

export const Icon = {
  dashboard: () => svg(<><rect x="3" y="3" width="7" height="9" rx="1" /><rect x="14" y="3" width="7" height="5" rx="1" /><rect x="14" y="12" width="7" height="9" rx="1" /><rect x="3" y="16" width="7" height="5" rx="1" /></>),
  sites: () => svg(<><rect x="3" y="4" width="18" height="16" rx="2" /><path d="M3 9h18M8 4v5" /></>),
  logs: () => svg(<><path d="M4 6h16M4 12h16M4 18h10" /></>),
  services: () => svg(gearPath),
  settings: () => svg(gearPath),
  firewall: () => svg(shieldPath),
  shield: () => svg(<>{shieldPath}<path d="M9 12l2 2 4-4" /></>),
  users: () => svg(<><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" /><path d="M23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" /></>),
  audit: () => svg(<><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>),
  theme: () => svg(<><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></>),
  logout: () => svg(<><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" /></>),
  plus: () => svg(<><path d="M12 5v14M5 12h14" /></>),
  external: () => svg(<><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6M15 3h6v6M10 14L21 3" /></>),

  // Site types
  wordpress: () => svg(<path d="M12 2 3 7v10l9 5 9-5V7l-9-5zM12 8v8M8.5 10l3.5 6 3.5-6" />),
  cart: () => svg(<><circle cx="9" cy="21" r="1" /><circle cx="19" cy="21" r="1" /><path d="M2.5 3h2l2.6 12.4a2 2 0 0 0 2 1.6h8a2 2 0 0 0 2-1.6L21 7H6" /></>),
  layers: () => svg(<><path d="M12 3 2 8l10 5 10-5-10-5z" /><path d="m2 13 10 5 10-5" /></>),
  code: () => svg(<><path d="m9 8-4 4 4 4M15 8l4 4-4 4" /></>),
  triangle: () => svg(<path d="M12 3 2 20h20L12 3z" />),
  swap: () => svg(<><path d="m17 2 4 4-4 4" /><path d="M3 11V9a4 4 0 0 1 4-4h14" /><path d="m7 22-4-4 4-4" /><path d="M21 13v2a4 4 0 0 1-4 4H3" /></>),
  server: () => svg(<><rect x="2" y="3" width="20" height="7" rx="1.5" /><rect x="2" y="14" width="20" height="7" rx="1.5" /><path d="M6 6.5h.01M6 17.5h.01" /></>),
  database: () => svg(<><ellipse cx="12" cy="5" rx="8" ry="3" /><path d="M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5" /><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3" /></>),

  // File browser
  folder: () => svg(<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z" />),
  file: () => svg(<><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><path d="M14 2v6h6" /></>),
  arrowUp: () => svg(<><path d="M12 19V5" /><path d="m5 12 7-7 7 7" /></>),
  chevronRight: () => svg(<path d="m9 18 6-6-6-6" />),
  chevronDown: () => svg(<path d="m6 9 6 6 6-6" />),
  refresh: () => svg(<><path d="M21 12a9 9 0 1 1-3-6.7" /><path d="M21 3v6h-6" /></>),
  close: () => svg(<path d="M18 6 6 18M6 6l12 12" />),
  copy: () => svg(<><rect x="9" y="9" width="12" height="12" rx="2" /><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" /></>),
  download: () => svg(<><path d="M12 3v12" /><path d="m7 10 5 5 5-5" /><path d="M5 21h14" /></>),
  upload: () => svg(<><path d="M12 21V9" /><path d="m7 14 5-5 5 5" /><path d="M5 3h14" /></>),
  lock: () => svg(<><rect x="4" y="11" width="16" height="10" rx="2" /><path d="M8 11V7a4 4 0 0 1 8 0v4" /></>),
  key: () => svg(<><circle cx="8" cy="15" r="4" /><path d="m10.6 12.4 8-8M15 7l2 2M18 4l2 2" /></>),
  globe: () => svg(<><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a15 15 0 0 1 0 18 15 15 0 0 1 0-18z" /></>),
  clock: () => svg(<><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>),
  terminal: () => svg(<><path d="m5 7 5 5-5 5" /><path d="M12 19h7" /></>),
  warning: () => svg(<><path d="M10.3 3.9 1.9 18a2 2 0 0 0 1.7 3h16.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" /><path d="M12 9v4M12 17h.01" /></>),
  check: () => svg(<path d="M20 6 9 17l-5-5" />),
  cpu: () => svg(<><rect x="6" y="6" width="12" height="12" rx="1" /><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3" /></>),
  memory: () => svg(<><path d="M6 19v-3M10 19v-3M14 19v-3M18 19v-3" /><rect x="4" y="6" width="16" height="10" rx="1.5" /></>),
  disk: () => svg(<><ellipse cx="12" cy="6" rx="8" ry="3" /><path d="M4 6v6c0 1.7 3.6 3 8 3s8-1.3 8-3V6" /><path d="M4 12v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6" /></>),
  bolt: () => svg(<path d="M13 2 3 14h7l-1 8 10-12h-7l1-8z" />),
  gauge: () => svg(<><path d="M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z" /><path d="M12 3a9 9 0 0 0-9 9c0 2.4.9 4.6 2.4 6.2" /><path d="M15 12a3 3 0 0 0-1.5-2.6L18 5" /></>),
  history: () => svg(<><path d="M3 3v5h5" /><path d="M3.05 13a9 9 0 1 0 2-6.4L3 8.5" /><path d="M12 7v5l4 2" /></>),
  trash: () => svg(<><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0-1 14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2L4 6" /><path d="M10 11v6M14 11v6" /></>),
};
