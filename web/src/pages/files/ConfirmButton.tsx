import { useState } from "react";
import { Button } from "../../components/ui";

/** A destructive action that needs a second click. */
export function ConfirmButton({
  label,
  confirmLabel,
  onConfirm,
  disabled,
}: {
  label: string;
  confirmLabel: string;
  onConfirm: () => void;
  disabled?: boolean;
}) {
  const [armed, setArmed] = useState(false);
  if (!armed) {
    return (
      <Button
        variant="danger"
        icon="trash"
        disabled={disabled}
        onClick={() => setArmed(true)}
      >
        {label}
      </Button>
    );
  }
  return (
    <span className="inline-flex items-center gap-1">
      <Button
        variant="danger"
        disabled={disabled}
        onClick={() => {
          setArmed(false);
          onConfirm();
        }}
      >
        {confirmLabel}
      </Button>
      <Button onClick={() => setArmed(false)}>Cancel</Button>
    </span>
  );
}
