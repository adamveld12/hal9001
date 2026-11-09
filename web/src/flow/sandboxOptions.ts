import type { SandboxSummary } from "../api/sandboxes";
import { SANDBOX_BACKENDS } from "../api/sandboxes";

export type SandboxOption = {
  /** The node's `sandbox` value; "" is none. */
  value: string;
  label: string;
  /** Undefined for "None". */
  enabled?: boolean;
};

/** Backends whose nodes can pick an image. */
const IMAGE_BACKENDS = ["docker", "kubernetes"];

export function hasImages(backend: string | undefined): boolean {
  return backend !== undefined && IMAGE_BACKENDS.includes(backend);
}

/** Dropdown options: None, then every backend in display order with its status. Local is always enabled. */
export function sandboxOptions(
  items: readonly SandboxSummary[] | undefined,
): SandboxOption[] {
  const byBackend = new Map((items ?? []).map((s) => [s.backend, s]));
  return [
    { value: "", label: "None (no file/shell access)" },
    ...SANDBOX_BACKENDS.map((b) => {
      const enabled =
        b.key === "local" || byBackend.get(b.key)?.enabled === true;
      return {
        value: b.key,
        label: `${b.label} (${enabled ? "Enabled" : "Disabled"})`,
        enabled,
      };
    }),
  ];
}

/** The option of the current value; an unknown value (a hand-edited file) is shown as is, disabled. */
export function selectedOption(
  options: readonly SandboxOption[],
  value: string | undefined,
): SandboxOption {
  return (
    options.find((o) => o.value === (value ?? "")) ?? {
      value: value ?? "",
      label: `${value} (unknown)`,
      enabled: false,
    }
  );
}

/** Image choices of a backend: its images, plus the node's current image when it is not among them. */
export function imageOptions(
  images: readonly string[] | undefined,
  current: string | undefined,
): string[] {
  const list = [...(images ?? [])];
  if (current && !list.includes(current)) list.unshift(current);
  return list;
}

/** Where the warning for a disabled backend links to. */
export function sandboxConfigPath(backend: string): string {
  return `/sandboxes/${backend}`;
}
