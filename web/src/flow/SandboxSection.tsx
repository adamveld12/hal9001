import { Link } from "react-router";
import {
  useSandbox,
  useSandboxes,
  type SandboxBackend,
} from "../api/sandboxes";
import { Badge, Field, Select, TextInput } from "../components/ui";
import {
  hasImages,
  imageOptions,
  sandboxConfigPath,
  sandboxOptions,
  selectedOption,
} from "./sandboxOptions";

/** Sandbox, image, repo and ref of an agent node. `errors` are the node's diagnostics per field. */
export function SandboxSection({
  sandbox,
  image,
  repo,
  gitRef,
  errors,
  onChange,
}: {
  sandbox?: string;
  image?: string;
  repo?: string;
  gitRef?: string;
  errors: { sandbox: string[]; image: string[]; repo: string[]; ref: string[] };
  onChange: (field: "sandbox" | "image" | "repo" | "ref", v: string) => void;
}) {
  const list = useSandboxes();
  const options = sandboxOptions(list.data);
  const current = selectedOption(options, sandbox);
  const withImages = hasImages(sandbox);
  const detail = useSandbox(
    (withImages ? sandbox : "docker") as SandboxBackend,
    {
      enabled: withImages,
    },
  );
  const images = imageOptions(detail.data?.config.images, image);
  return (
    <>
      <Field label="Sandbox" error={errors.sandbox}>
        <div className="flex items-center gap-2">
          <Select
            aria-label="Sandbox"
            className="flex-1"
            value={current.value}
            invalid={errors.sandbox.length > 0}
            onChange={(e) => onChange("sandbox", e.target.value)}
          >
            {!options.some((o) => o.value === current.value) && (
              <option value={current.value}>{current.label}</option>
            )}
            {options.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </Select>
          {current.enabled !== undefined && (
            <Badge tone={current.enabled ? "ok" : "warn"}>
              {current.enabled ? "Enabled" : "Disabled"}
            </Badge>
          )}
        </div>
        {sandbox && current.enabled === false && (
          <p className="mt-1 text-xs text-warn" data-testid="sandbox-disabled">
            This sandbox is not enabled, so the node will fail to run.{" "}
            <Link className="underline" to={sandboxConfigPath(sandbox)}>
              Enable it in Configure → Sandboxes
            </Link>
            .
          </p>
        )}
      </Field>
      {withImages && (
        <Field
          label="Image"
          error={errors.image}
          hint="The first image is the default."
        >
          <Select
            aria-label="Image"
            value={image ?? ""}
            invalid={errors.image.length > 0}
            onChange={(e) => onChange("image", e.target.value)}
          >
            <option value="">
              (default{images[0] ? `: ${images[0]}` : ""})
            </option>
            {images.map((i) => (
              <option key={i} value={i}>
                {i}
              </option>
            ))}
          </Select>
        </Field>
      )}
      {sandbox && (
        <div className="grid grid-cols-2 gap-2">
          <Field
            label="Repo"
            error={errors.repo}
            hint="Cloned into the sandbox."
          >
            <TextInput
              value={repo ?? ""}
              invalid={errors.repo.length > 0}
              onChange={(e) => onChange("repo", e.target.value)}
            />
          </Field>
          <Field
            label="Ref"
            error={errors.ref}
            hint="Branch, tag or commit; needs a repo."
          >
            <TextInput
              value={gitRef ?? ""}
              invalid={errors.ref.length > 0}
              onChange={(e) => onChange("ref", e.target.value)}
            />
          </Field>
        </div>
      )}
    </>
  );
}
