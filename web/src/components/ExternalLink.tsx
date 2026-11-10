import type { AnchorHTMLAttributes } from "react";
import { externalLinkProps } from "../lib/links";

/** Plain anchor that opens outbound URLs in a new tab; same-origin hrefs stay in the tab. */
export function ExternalLink({
  href,
  ...rest
}: AnchorHTMLAttributes<HTMLAnchorElement>) {
  return <a href={href} {...externalLinkProps(href)} {...rest} />;
}
