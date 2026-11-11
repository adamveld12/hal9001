import { describe, expect, it } from "vitest";
import type { SandboxConfig } from "../../api/sandboxes";
import {
  configFromForm,
  formFromConfig,
  parseKeepAlive,
  textToList,
} from "./sandboxForm";

const base: SandboxConfig = {
  enabled: true,
  lifecycle: "per_run",
  approval: "rules",
  approval_rules: { shell: [], fs_write: [] },
  timeout: "10m",
  keep_alive: "1m",
  resources: { cpus: 0, memory_mb: 0, disk_gb: 0 },
  root: "",
  socket: "",
  images: ["alpine:3"],
  network: true,
  org_id: "",
  image_ref: "",
  architecture: "",
  timeout_minutes: 0,
  disable_tailnet: false,
  disable_depot_cache: false,
  secrets: { DB_URL: "PROD_DB" },
  endpoint: "",
  kubeconfig: "",
  context: "",
  namespace: "",
  service_account: "",
  host: "",
  port: 22,
  user: "",
  key_file: "",
  host_key: "",
};

describe("sandbox form", () => {
  it("round-trips a config", () => {
    const form = formFromConfig(base);
    expect(form.imagesText).toBe("alpine:3");
    expect(form.secretsText).toBe("DB_URL=PROD_DB");
    expect(configFromForm(form)).toEqual({ config: base });
  });

  it("parses typed lists and numbers", () => {
    const form = {
      ...formFromConfig(base),
      imagesText: " a:1 \n\n b:2\n",
      secretsText: "X=y\nZ = w",
      cpus: "1,5",
      memory_mb: "512",
      port: "2222",
    };
    const r = configFromForm(form);
    if (!("config" in r)) throw new Error("unexpected errors");
    expect(r.config.images).toEqual(["a:1", "b:2"]);
    expect(r.config.secrets).toEqual({ X: "y", Z: "w" });
    expect(r.config.resources).toEqual({
      cpus: 1.5,
      memory_mb: 512,
      disk_gb: 0,
    });
    expect(r.config.port).toBe(2222);
  });

  it("reports malformed fields", () => {
    const form = {
      ...formFromConfig(base),
      secretsText: "no-equals",
      port: "2x",
    };
    const r = configFromForm(form);
    if (!("errors" in r)) throw new Error("expected errors");
    expect(Object.keys(r.errors).sort()).toEqual(["port", "secretsText"]);
  });

  it("splits lines", () => {
    expect(textToList("a\n  \n b ")).toEqual(["a", "b"]);
  });

  it("validates keep_alive like the server", () => {
    for (const ok of ["30s", "1m", "2h", "1d", "1h30m", "1.5h", " 5m "]) {
      expect(parseKeepAlive(ok), ok).toEqual({ value: ok.trim() });
    }
    // 0 is allowed and sent with a unit: the API's duration format needs one.
    expect(parseKeepAlive("0")).toEqual({ value: "0s" });
    expect(parseKeepAlive("0s")).toEqual({ value: "0s" });
    expect(parseKeepAlive("0m")).toEqual({ value: "0m" });
    for (const bad of ["", "  ", "-1m", "30", "soon", "1 m", "m", "1m 30s"]) {
      expect("error" in parseKeepAlive(bad), `"${bad}"`).toBe(true);
    }
    expect(parseKeepAlive("-1m")).toEqual({
      error: "Keep alive must not be negative",
    });
  });

  it("puts keep_alive in the config and reports a bad one", () => {
    const r = configFromForm({ ...formFromConfig(base), keep_alive: "0" });
    if (!("config" in r)) throw new Error("unexpected errors");
    expect(r.config.keep_alive).toBe("0s");

    const bad = configFromForm({ ...formFromConfig(base), keep_alive: "-5m" });
    if (!("errors" in bad)) throw new Error("expected errors");
    expect(Object.keys(bad.errors)).toEqual(["keep_alive"]);
  });
});
