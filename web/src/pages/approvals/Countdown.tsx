import { cx } from "../../lib/cx";
import { TONE_TEXT } from "../../lib/status";
import { timeLeft, useNow } from "./timeLeft";

/** Live time left until `expiresAt`, coloured by urgency. */
export function Countdown({
  expiresAt,
  className,
}: {
  expiresAt: string | null;
  className?: string;
}) {
  const left = timeLeft(expiresAt, useNow());
  if (!left) return null;
  return (
    <span
      className={cx("tabular-nums", TONE_TEXT[left.tone], className)}
      title={left.expired ? "Past its approval timeout" : "Time left to decide"}
    >
      {left.label}
    </span>
  );
}
