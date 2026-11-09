import { describe, expect, it } from "vitest";
import {
  hasImages,
  imageOptions,
  sandboxConfigPath,
  sandboxOptions,
  selectedOption,
} from "./sandboxOptions";

const summary = (backend: string, enabled: boolean) =>
  ({
    backend,
    enabled,
    configured: true,
    valid: true,
    lifecycle: "per_run",
    approval: "rules",
  }) as never;

describe("sandbox options", () => {
  it("lists None then every backend with its status; local is always enabled", () => {
    const opts = sandboxOptions([
      summary("local", false),
      summary("docker", true),
      summary("kubernetes", false),
    ]);
    expect(opts.map((o) => [o.value, o.label])).toEqual([
      ["", "None (no file/shell access)"],
      ["local", "Local (Enabled)"],
      ["docker", "Docker (Enabled)"],
      ["depot", "Depot (Disabled)"],
      ["kubernetes", "Kubernetes (Disabled)"],
      ["ssh", "SSH (Disabled)"],
    ]);
    expect(opts[0].enabled).toBeUndefined();
  });

  it("shows an unknown current value as a disabled option", () => {
    const opts = sandboxOptions([]);
    expect(selectedOption(opts, "kubernetes").enabled).toBe(false);
    expect(selectedOption(opts, undefined).value).toBe("");
    expect(selectedOption(opts, "nope")).toMatchObject({ enabled: false });
  });

  it("offers images for docker and kubernetes only, keeping a current unlisted image", () => {
    expect(hasImages("docker")).toBe(true);
    expect(hasImages("kubernetes")).toBe(true);
    expect(hasImages("ssh")).toBe(false);
    expect(imageOptions(["a", "b"], "b")).toEqual(["a", "b"]);
    expect(imageOptions(["a"], "z")).toEqual(["z", "a"]);
    expect(sandboxConfigPath("kubernetes")).toBe("/sandboxes/kubernetes");
  });
});
