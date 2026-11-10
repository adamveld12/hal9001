import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Markdown } from "./Markdown";

describe("Markdown links", () => {
  it("opens external links in a new tab", () => {
    const html = renderToStaticMarkup(
      <Markdown>{"[a](https://example.com/x)"}</Markdown>,
    );
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener noreferrer"');
  });
  it("keeps internal links in the tab", () => {
    const html = renderToStaticMarkup(
      <Markdown>{"[a](/inbox) [b](#x)"}</Markdown>,
    );
    expect(html).not.toContain("target=");
  });
});
