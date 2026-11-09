import { describe, expect, it } from "vitest";
import { withDescription } from "./description";

describe("withDescription", () => {
  it("adds the entry below the leading comments", () => {
    expect(withDescription("# note\nlang: bash\n", "Polls things")).toBe(
      '# note\ndescription: "Polls things"\nlang: bash\n',
    );
  });
  it("replaces an existing entry in place, including continuation lines", () => {
    const yaml = "description: >\n  old\n  text\nlang: bash\n";
    expect(withDescription(yaml, "new")).toBe("description: new\nlang: bash\n");
  });
  it("removes the entry for a blank description", () => {
    expect(
      withDescription('lang: bash\ndescription: "x"\nkey: k\n', "  "),
    ).toBe("lang: bash\nkey: k\n");
  });
  it("quotes text that YAML would read as another type", () => {
    expect(withDescription("lang: bash\n", "yes")).toBe(
      'description: "yes"\nlang: bash\n',
    );
  });
});
