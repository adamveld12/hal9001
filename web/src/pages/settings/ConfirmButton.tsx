import { useEffect, useState, type ReactNode } from "react";
import { Button } from "../../components/ui";
import type { IconName } from "../../components/icons";

/** Destructive action that needs a second click within a few seconds. */
export function ConfirmButton({
  children,
  confirmLabel = "Confirm",
  icon,
  disabled,
  onConfirm,
}: {
  children: ReactNode;
  confirmLabel?: string;
  icon?: IconName;
  disabled?: boolean;
  onConfirm: () => void;
}) {
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const t = setTimeout(() => setArmed(false), 4000);
    return () => clearTimeout(t);
  }, [armed]);

  return (
    <Button
      variant="danger"
      icon={icon}
      disabled={disabled}
      onClick={() => {
        if (armed) {
          setArmed(false);
          onConfirm();
        } else {
          setArmed(true);
        }
      }}
    >
      {armed ? confirmLabel : children}
    </Button>
  );
}
