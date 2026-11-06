// Transform node types. They alias the generated contract so the editor model
// cannot drift from api/openapi.yaml.

import type { components } from "../../api/schema";

type S = components["schemas"];

export type TransformNode = S["TransformNode"];
export type TransformStep = S["TransformStep"];
export type TransformCondition = S["TransformCondition"];
export type TransformRule = S["TransformRule"];
export type TransformValue = S["TransformValue"];
export type TransformOperator = S["TransformOperator"];
export type TransformMatch = S["TransformMatch"];
export type StepKind = TransformStep["type"];
export type ValueKind = TransformValue["kind"];
export type ValueFunction = NonNullable<TransformValue["function"]>;
export type AggregateOp = NonNullable<TransformStep["op"]>;
export type TransformPreview = S["TransformPreview"];
export type TransformStepResult = S["TransformStepResult"];
export type TransformCompiled = S["TransformCompiled"];
export type FlowSample = S["FlowSample"];

/** A JSON scalar as used in condition values and constants. */
export type Scalar = string | number | boolean;

export const STEP_KINDS: readonly StepKind[] = [
  "filter",
  "route",
  "pick",
  "set",
  "remove",
  "split",
  "sort",
  "limit",
  "unique",
  "aggregate",
  "filter_list",
];
