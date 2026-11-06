import { useState } from "react";
import { compileTransform } from "../../api/transform";
import { errorMessage } from "../../api/client";
import { Button } from "../../components/ui";
import { Modal } from "../../pages/timeline/Modal";
import type { TransformStep } from "./types";

/** Confirms turning the node into a jq code node, then compiles the steps. */
export function ConvertDialog({
  steps,
  onClose,
  onConverted,
}: {
  steps: TransformStep[];
  onClose: () => void;
  onConverted: (jq: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const convert = () => {
    setBusy(true);
    setError(undefined);
    compileTransform(steps).then(
      (c) => onConverted(c.jq),
      (e) => {
        setBusy(false);
        setError(errorMessage(e));
      },
    );
  };

  return (
    <Modal
      title="Convert to code node"
      onClose={busy ? () => undefined : onClose}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={convert} disabled={busy}>
            {busy ? "Converting…" : "Convert to code node"}
          </Button>
        </>
      }
    >
      <p className="m-0 text-xs">
        This replaces the steps with the jq program they compile to, in a code
        node with the same name, position and connections.
      </p>
      <p className="m-0 text-xs font-medium">
        It cannot be converted back to steps. You can still undo it until you
        save.
      </p>
      {error && (
        <p role="alert" className="m-0 text-xs text-bad">
          {error}
        </p>
      )}
    </Modal>
  );
}
