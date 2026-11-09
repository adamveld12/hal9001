// Where the daemon files the sensors a flow session drafted (`sensors/<id>.yaml`)
// and how their findings are told apart from the flow's. Pure.

import type { Diagnostic } from "../files/diagnostics";

const SENSOR_FILE = /^sensors\/([^/]+)\.yaml$/;

/** The `file` of the findings about draft sensor `id`. */
export function sensorDraftFile(id: string): string {
  return `sensors/${id}.yaml`;
}

/** "sensor a: " for a finding about draft sensor `a`, so it is not read as the flow's; empty for any other finding. */
export function findingLabel(d: Pick<Diagnostic, "file">): string {
  const id = SENSOR_FILE.exec(d.file ?? "")?.[1];
  return id === undefined ? "" : `sensor ${id}: `;
}

/**
 * The draft's findings for the canvas, whose problem list shows a message
 * alone and presents what it has no node for as the flow's: a sensor's
 * messages say which sensor they are about.
 */
export function canvasDiagnostics(
  diagnostics: readonly Diagnostic[],
): Diagnostic[] {
  return diagnostics.map((d) => {
    const label = findingLabel(d);
    return label === "" ? d : { ...d, message: `${label}${d.message}` };
  });
}
