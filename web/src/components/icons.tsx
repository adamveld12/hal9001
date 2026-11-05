import type { SVGProps } from "react";

// Minimal stroke icons (24px grid), drawn inline to avoid an icon dependency.
const PATHS = {
  timeline: "M4 6h16M4 12h10M4 18h14",
  inbox: "M4 13l2-8h12l2 8M4 13v6h16v-6M4 13h5l1 2h4l1-2h5",
  approvals: "M5 12l4 4 10-10",
  flows: "M6 5v4a3 3 0 003 3h6a3 3 0 013 3v4M6 5H4m2 0h2m8 14h4m-4 0h-2",
  sensors:
    "M12 12m-2 0a2 2 0 104 0 2 2 0 10-4 0M7.5 7.5a6 6 0 000 9M16.5 7.5a6 6 0 010 9",
  prompts: "M5 4h14v16H5zM8 9h8M8 13h8M8 17h5",
  settings:
    "M12 15a3 3 0 100-6 3 3 0 000 6zM19 12a7 7 0 00-.1-1.2l2-1.5-2-3.5-2.3.9a7 7 0 00-2-1.2L14.2 3h-4l-.4 2.5a7 7 0 00-2 1.2l-2.3-.9-2 3.5 2 1.5a7 7 0 000 2.4l-2 1.5 2 3.5 2.3-.9a7 7 0 002 1.2l.4 2.5h4l.4-2.5a7 7 0 002-1.2l2.3.9 2-3.5-2-1.5c.1-.4.1-.8.1-1.2z",
  command: "M5 7l5 5-5 5M12 17h7",
  agent:
    "M12 4v3M7 7h10a2 2 0 012 2v7a2 2 0 01-2 2H7a2 2 0 01-2-2V9a2 2 0 012-2zM9.5 12v1.5M14.5 12v1.5",
  note: "M6 4h12v16l-6-4-6 4z",
  sensor: "M4 12h4l2-6 4 12 2-6h4",
  warning: "M12 4l9 16H3zM12 10v4M12 17v.5",
  error: "M12 3a9 9 0 100 18 9 9 0 000-18zM9 9l6 6M15 9l-6 6",
  check: "M5 12l4 4 10-10",
  close: "M6 6l12 12M18 6L6 18",
  plus: "M12 5v14M5 12h14",
  undo: "M9 7L4 12l5 5M4 12h11a5 5 0 010 10h-3",
  redo: "M15 7l5 5-5 5M20 12H9a5 5 0 000 10h3",
  sun: "M12 8a4 4 0 100 8 4 4 0 000-8zM12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5L19 19M5 19l1.5-1.5M17.5 6.5L19 5",
  moon: "M20 14A8 8 0 0110 4a8 8 0 1010 10z",
  play: "M7 5l12 7-12 7z",
  trash: "M5 7h14M10 7V4h4v3M7 7l1 13h8l1-13",
  search: "M11 4a7 7 0 100 14 7 7 0 000-14zM20 20l-4-4",
  star: "M12 4l2.5 5 5.5.8-4 3.9.9 5.5-4.9-2.6-4.9 2.6.9-5.5-4-3.9 5.5-.8z",
  archive: "M4 5h16v4H4zM6 9v10h12V9M10 13h4",
  external: "M14 4h6v6M20 4l-9 9M18 14v6H4V6h6",
  stop: "M6 6h12v12H6z",
  pause: "M8 5v14M16 5v14",
  refresh: "M20 6v5h-5M4 18v-5h5M18.5 9A7 7 0 005.6 8M5.5 15a7 7 0 0012.9 1",
  save: "M5 4h11l3 3v13H5zM8 4v5h7V4M8 20v-6h8v6",
} as const;

export type IconName = keyof typeof PATHS;

export function Icon({
  name,
  size = 16,
  ...rest
}: { name: IconName; size?: number } & Omit<SVGProps<SVGSVGElement>, "name">) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...rest}
    >
      <path d={PATHS[name]} />
    </svg>
  );
}
