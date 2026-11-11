/** Where the old Settings > Tailscale page now lives. */
export const TAILSCALE_REDIRECT = "/settings/auth#tailscale";

/** The element id a URL hash points at (`#tailscale` gives `tailscale`); null for no or an empty hash. */
export function scrollTargetId(hash: string): string | null {
  if (!hash.startsWith("#") || hash.length < 2) return null;
  try {
    return decodeURIComponent(hash.slice(1));
  } catch {
    return hash.slice(1);
  }
}
