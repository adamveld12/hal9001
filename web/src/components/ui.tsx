import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  Ref,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from "react";
import { cx, defaultFullWidth } from "../lib/cx";
import { statusTone, TONE_BADGE, type Tone } from "../lib/status";
import { Icon, type IconName } from "./icons";

type Variant = "default" | "primary" | "danger" | "ghost";

const BTN: Record<Variant, string> = {
  default: "border-line bg-panel hover:bg-raised",
  primary: "border-accent bg-accent text-accent-fg hover:opacity-90",
  danger: "border-bad/50 bg-bad/10 text-bad hover:bg-bad/20",
  ghost: "border-transparent hover:bg-raised",
};

export function Button({
  variant = "default",
  icon,
  className,
  children,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
  icon?: IconName;
}) {
  return (
    <button
      type="button"
      className={cx(
        "inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-md border px-2.5 text-xs font-medium",
        "disabled:cursor-not-allowed disabled:opacity-50",
        BTN[variant],
        className,
      )}
      {...rest}
    >
      {icon && <Icon name={icon} size={14} />}
      {children}
    </button>
  );
}

export function Badge({
  tone = "muted",
  children,
  className,
}: {
  tone?: Tone;
  children: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cx(
        "inline-flex items-center rounded border px-1.5 text-[11px] leading-4",
        TONE_BADGE[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

/** Badge coloured from an API status/decision enum. */
export function StatusBadge({ status }: { status: string }) {
  return <Badge tone={statusTone(status)}>{status.replaceAll("_", " ")}</Badge>;
}

export function Spinner({ label = "Loading" }: { label?: string }) {
  return (
    <span role="status" className="inline-flex items-center gap-2 text-muted">
      <span className="size-3.5 animate-spin rounded-full border-2 border-line border-t-accent" />
      {label}…
    </span>
  );
}

export function LoadingState({ label }: { label?: string }) {
  return (
    <div className="flex h-40 items-center justify-center">
      <Spinner label={label} />
    </div>
  );
}

export function EmptyState({
  title,
  hint,
  action,
}: {
  title: string;
  hint?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-16 text-center">
      <div className="text-sm font-medium">{title}</div>
      {hint && <div className="max-w-md text-muted">{hint}</div>}
      {action}
    </div>
  );
}

export function ErrorState({
  error,
  onRetry,
}: {
  error: unknown;
  onRetry?: () => void;
}) {
  const message = error instanceof Error ? error.message : String(error);
  return (
    <div
      role="alert"
      className="m-4 rounded-md border border-bad/40 bg-bad/5 p-4"
    >
      <div className="flex items-center gap-2 font-medium text-bad">
        <Icon name="error" size={16} />
        Request failed
      </div>
      <div className="mt-1 break-words font-mono text-xs text-muted">
        {message}
      </div>
      {onRetry && (
        <Button className="mt-3" onClick={onRetry} icon="refresh">
          Retry
        </Button>
      )}
    </div>
  );
}

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex min-h-12 items-center gap-3 border-b border-line px-4 py-2">
      <div className="min-w-0">
        <h1 className="m-0 truncate text-sm font-semibold">{title}</h1>
        {subtitle && (
          <div className="truncate text-xs text-muted">{subtitle}</div>
        )}
      </div>
      <div className="ml-auto flex items-center gap-2">{actions}</div>
    </div>
  );
}

export function Panel({
  title,
  actions,
  children,
  className,
}: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section
      className={cx("rounded-md border border-line bg-panel", className)}
    >
      {title && (
        <header className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2 text-xs font-semibold">
          {title}
          <span className="ml-auto flex items-center gap-2 font-normal">
            {actions}
          </span>
        </header>
      )}
      <div className="p-3">{children}</div>
    </section>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="rounded border border-line bg-raised px-1 font-mono text-[10px] text-muted">
      {children}
    </kbd>
  );
}

export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: ReactNode;
  /** Server or client diagnostic message for this field. */
  error?: string[];
  children: ReactNode;
}) {
  const bad = error !== undefined && error.length > 0;
  return (
    <label className="block space-y-1" data-field={label}>
      <span className="text-[11px] font-medium text-muted">{label}</span>
      {children}
      {hint && !bad && (
        <span className="block text-[11px] text-muted">{hint}</span>
      )}
      {bad &&
        error.map((m) => (
          <span key={m} className="block text-[11px] text-bad">
            {m}
          </span>
        ))}
    </label>
  );
}

const CONTROL =
  "rounded-md border border-line bg-panel px-2 py-1 text-xs placeholder:text-muted/70 disabled:opacity-60";

export function TextInput({
  invalid,
  className,
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }) {
  return (
    <input
      className={cx(
        defaultFullWidth(className),
        CONTROL,
        "h-7",
        invalid && "border-bad",
        className,
      )}
      {...rest}
    />
  );
}

export function TextArea({
  invalid,
  className,
  ...rest
}: TextareaHTMLAttributes<HTMLTextAreaElement> & {
  invalid?: boolean;
  ref?: Ref<HTMLTextAreaElement>;
}) {
  return (
    <textarea
      className={cx(
        defaultFullWidth(className),
        CONTROL,
        "font-mono",
        invalid && "border-bad",
        className,
      )}
      {...rest}
    />
  );
}

export function Select({
  invalid,
  className,
  children,
  ...rest
}: SelectHTMLAttributes<HTMLSelectElement> & { invalid?: boolean }) {
  return (
    <select
      className={cx(
        defaultFullWidth(className),
        CONTROL,
        "h-7",
        invalid && "border-bad",
        className,
      )}
      {...rest}
    >
      {children}
    </select>
  );
}

export function Toggle({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <label className="inline-flex cursor-pointer items-center gap-2 text-xs">
      <input
        type="checkbox"
        role="switch"
        className="peer sr-only"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span
        aria-hidden="true"
        className={cx(
          "relative h-4 w-7 rounded-full border border-line transition-colors peer-focus-visible:outline-2 peer-focus-visible:outline-accent",
          checked ? "bg-accent" : "bg-raised",
        )}
      >
        <span
          className={cx(
            "absolute top-0.5 size-2.5 rounded-full bg-panel transition-all",
            checked ? "left-3.5" : "left-0.5 bg-muted",
          )}
        />
      </span>
      {label}
    </label>
  );
}

export function Table({ children }: { children: ReactNode }) {
  return <table className="w-full border-collapse text-xs">{children}</table>;
}

export function Th({
  children,
  className,
}: {
  children?: ReactNode;
  className?: string;
}) {
  return (
    <th
      className={cx(
        "sticky top-0 border-b border-line bg-bg px-3 py-1.5 text-left text-[11px] font-medium text-muted",
        className,
      )}
    >
      {children}
    </th>
  );
}

export function Td({
  children,
  className,
}: {
  children?: ReactNode;
  className?: string;
}) {
  return (
    <td
      className={cx("border-b border-line px-3 py-1.5 align-middle", className)}
    >
      {children}
    </td>
  );
}
