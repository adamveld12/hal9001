/** True for an absolute http(s)/mailto URL (or protocol-relative) whose origin differs from the app's. */
export function isExternalHref(
  href: string | undefined,
  origin: string = window.location.origin,
): boolean {
  if (!href) return false;
  const h = href.trim();
  if (/^mailto:/i.test(h)) return true;
  if (!/^(https?:)?\/\//i.test(h)) return false;
  try {
    const u = new URL(h, origin);
    return (
      (u.protocol === "http:" || u.protocol === "https:") && u.origin !== origin
    );
  } catch {
    return false;
  }
}

/** Anchor attributes for an href: new tab for outbound links, none for internal ones. */
export function externalLinkProps(href: string | undefined) {
  return isExternalHref(href)
    ? { target: "_blank", rel: "noopener noreferrer" }
    : {};
}
