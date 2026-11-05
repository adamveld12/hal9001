import { useState } from "react";
import {
  usePauseKillSwitch,
  useResumeKillSwitch,
  type Status,
} from "../api/status";
import { errorMessage } from "../api/client";
import { Icon } from "../components/icons";
import { Button } from "../components/ui";
import { cx } from "../lib/cx";

/** Header kill switch: pause (optionally killing running work) or resume. */
export function KillSwitch({ status }: { status: Status | undefined }) {
  const pause = usePauseKillSwitch();
  const resume = useResumeKillSwitch();
  const [open, setOpen] = useState(false);
  const paused = status?.kill_switch.paused === true;
  const error = pause.error ?? resume.error;

  if (paused) {
    return (
      <div className="flex items-center gap-2">
        <span
          className="inline-flex items-center gap-1 rounded-md border border-bad/40 bg-bad/10 px-2 py-1 text-xs font-medium text-bad"
          title={status?.kill_switch.reason ?? undefined}
        >
          <Icon name="stop" size={12} />
          Paused{status?.kill_switch.kill_running ? " (killed running)" : ""}
        </span>
        <Button
          variant="primary"
          icon="play"
          disabled={resume.isPending}
          onClick={() => resume.mutate()}
        >
          Resume
        </Button>
        {error && (
          <span className="text-xs text-bad">{errorMessage(error)}</span>
        )}
      </div>
    );
  }

  return (
    <div className="relative">
      <Button
        aria-haspopup="menu"
        aria-expanded={open}
        disabled={status === undefined}
        onClick={() => setOpen((v) => !v)}
        className="text-ok"
      >
        <span className="size-2 rounded-full bg-ok" />
        Running
      </Button>
      {open && (
        <div
          role="menu"
          className="absolute right-0 top-9 z-30 w-64 rounded-md border border-line bg-panel p-1 shadow-lg"
          onKeyDown={(e) => e.key === "Escape" && setOpen(false)}
        >
          {[
            {
              label: "Pause",
              hint: "Stop sensors and new nodes; let running work finish",
              kill: false,
            },
            {
              label: "Pause and kill running",
              hint: "Also SIGTERM running processes",
              kill: true,
            },
          ].map((o) => (
            <button
              key={o.label}
              type="button"
              role="menuitem"
              className={cx(
                "block w-full rounded px-2 py-1.5 text-left hover:bg-raised",
                o.kill && "text-bad",
              )}
              onClick={() => {
                setOpen(false);
                pause.mutate({ kill_running: o.kill });
              }}
            >
              <div className="text-xs font-medium">{o.label}</div>
              <div className="text-[11px] text-muted">{o.hint}</div>
            </button>
          ))}
        </div>
      )}
      {error && (
        <span className="ml-2 text-xs text-bad">{errorMessage(error)}</span>
      )}
    </div>
  );
}
