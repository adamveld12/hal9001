import { describe, expect, it } from "vitest";
import { yamlBlock, yamlList, yamlScalar } from "./yaml";

describe("yamlScalar", () => {
  it("leaves unambiguous words and durations plain", () => {
    expect(yamlScalar("github-notifications")).toBe("github-notifications");
    expect(yamlScalar("5m")).toBe("5m");
    expect(yamlScalar("bash")).toBe("bash");
  });

  it("quotes values YAML would read as another type", () => {
    for (const v of [
      "true",
      "No",
      "null",
      "123",
      "1e3",
      "0x1F",
      "1_000",
      "~",
    ]) {
      expect(yamlScalar(v)).toBe(JSON.stringify(v));
    }
  });

  it("quotes templates, colons, quotes, and empty strings", () => {
    expect(yamlScalar("{{.id}}:{{.updated_at}}")).toBe(
      '"{{.id}}:{{.updated_at}}"',
    );
    expect(yamlScalar('say "hi"')).toBe('"say \\"hi\\""');
    expect(yamlScalar("")).toBe('""');
    expect(yamlScalar("12:30")).toBe('"12:30"');
  });
});

describe("yamlBlock", () => {
  it("indents lines and drops trailing blank lines", () => {
    expect(yamlBlock("script", "gh api x\n\njq .\n\n")).toEqual([
      "script: |",
      "  gh api x",
      "",
      "  jq .",
    ]);
  });

  it("adds an indentation indicator when the first line is indented", () => {
    expect(yamlBlock("script", "  indented\nplain")[0]).toBe("script: |2");
  });

  it("normalises CRLF and writes empty text as an empty string", () => {
    expect(yamlBlock("script", "a\r\nb")).toEqual(["script: |", "  a", "  b"]);
    expect(yamlBlock("script", " \n")).toEqual(['script: ""']);
  });
});

describe("yamlList", () => {
  it("quotes entries as needed", () => {
    expect(yamlList("command", ["curl", "-s", "https://x/y?a=b"])).toEqual([
      "command:",
      "  - curl",
      '  - "-s"',
      '  - "https://x/y?a=b"',
    ]);
  });
});
