import {
  Fragment,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent,
} from "react";
import { useNavigate } from "react-router";
import { useConversationList } from "../../api/conversations";
import { useFlows } from "../../api/flows";
import { usePrompts } from "../../api/prompts";
import { useSensors } from "../../api/sensors";
import { useTools } from "../../api/tools";
import { Icon } from "../../components/icons";
import { Kbd } from "../../components/ui";
import { itemCommands } from "../../lib/commands/items";
import { search, type Match } from "../../lib/commands/search";
import { shortcutKeys } from "../../lib/commands/shortcuts";
import { cx } from "../../lib/cx";
import { chatPath } from "../../pages/chat/path";
import { MAC } from "../platform";
import { useCommands, useRegisterCommands } from "./context";

/** Registers the flows, sensors, tools, prompts and conversations while the palette is open; the lists are fetched when it opens (cached lists are reused). */
function PaletteItems() {
  const navigate = useNavigate();
  const flows = useFlows();
  const sensors = useSensors();
  const tools = useTools();
  const prompts = usePrompts();
  const conversations = useConversationList({});
  const commands = useMemo(
    () =>
      itemCommands(
        {
          flows: flows.data,
          sensors: sensors.data?.items,
          tools: tools.data?.items,
          prompts: prompts.data?.items,
          conversations: conversations.data?.pages[0]?.items,
        },
        (path) => void navigate(path),
        chatPath,
      ),
    [
      flows.data,
      sensors.data,
      tools.data,
      prompts.data,
      conversations.data,
      navigate,
    ],
  );
  useRegisterCommands("items", commands);
  return null;
}

/** The title with the matched characters emphasised. */
function Title({ text, indices }: { text: string; indices: number[] }) {
  if (indices.length === 0) return <>{text}</>;
  const hit = new Set(indices);
  return (
    <>
      {Array.from(text).map((c, i) =>
        hit.has(i) ? (
          <span key={i} className="font-semibold text-accent">
            {c}
          </span>
        ) : (
          <Fragment key={i}>{c}</Fragment>
        ),
      )}
    </>
  );
}

function Shortcut({ shortcut }: { shortcut: string }) {
  const keys = shortcutKeys(shortcut, MAC);
  const mod = shortcut.startsWith("mod+");
  return (
    <span
      className="ml-auto flex shrink-0 items-center gap-1"
      data-testid="shortcut-hint"
    >
      {keys.map((k, i) => (
        <Fragment key={i}>
          {!mod && i > 0 && (
            <span className="text-[10px] text-muted">then</span>
          )}
          <Kbd>{k}</Kbd>
        </Fragment>
      ))}
    </span>
  );
}

/**
 * The command palette: a modal over everything, also over the Ask AI drawer.
 * A native modal dialog gives the focus trap and sits in the top layer; focus
 * goes back to where it was when the palette closes.
 */
export function CommandPalette() {
  const { registry, recent, closePalette, execute } = useCommands();
  const commands = useSyncExternalStore(
    registry.subscribe,
    registry.getSnapshot,
  );
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const dialog = useRef<HTMLDialogElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const id = useId();

  useEffect(() => {
    const previous = document.activeElement;
    const d = dialog.current;
    if (d && !d.open) d.showModal();
    input.current?.focus();
    return () => {
      d?.close();
      if (previous instanceof HTMLElement && previous.isConnected) {
        previous.focus();
      }
    };
  }, []);

  const result = useMemo(
    () => search(query, commands, recent),
    [query, commands, recent],
  );
  const index = Math.min(active, Math.max(result.flat.length - 1, 0));
  const selected = result.flat[index];
  const optionId = (i: number) => `${id}-option-${i}`;
  const activeId = selected ? optionId(index) : undefined;

  useEffect(() => {
    if (activeId) {
      document.getElementById(activeId)?.scrollIntoView({ block: "nearest" });
    }
  }, [activeId, result]);

  const run = (m: Match | undefined) => {
    if (!m || m.command.disabled !== undefined) return;
    const arg = m.command.acceptsText ? result.arg : undefined;
    closePalette();
    execute(m.command.id, { arg, source: "palette" });
  };

  const onKeyDown = (e: KeyboardEvent) => {
    const n = result.flat.length;
    switch (e.key) {
      case "Escape":
        // Handled here so the drawer behind does not take the key and close too.
        e.preventDefault();
        e.stopPropagation();
        closePalette();
        break;
      case "ArrowDown":
        e.preventDefault();
        if (n > 0) setActive((index + 1) % n);
        break;
      case "ArrowUp":
        e.preventDefault();
        if (n > 0) setActive((index - 1 + n) % n);
        break;
      case "Enter":
        if (e.nativeEvent.isComposing) break;
        e.preventDefault();
        run(selected);
        break;
      case "Tab":
        e.preventDefault();
        break;
    }
  };

  // Position of each section's first item in the flat list the arrow keys walk.
  const starts = result.sections.map((_, si) =>
    result.sections.slice(0, si).reduce((n, x) => n + x.items.length, 0),
  );
  return (
    <dialog
      ref={dialog}
      aria-label="Command palette"
      data-testid="command-palette"
      onCancel={(e) => {
        e.preventDefault();
        closePalette();
      }}
      onKeyDown={onKeyDown}
      className="fixed left-1/2 top-[12vh] m-0 w-[40rem] max-w-[92vw] -translate-x-1/2 overflow-hidden rounded-lg border border-line bg-panel p-0 text-fg shadow-2xl backdrop:bg-black/40"
    >
      <PaletteItems />
      <div className="flex items-center gap-2 border-b border-line px-3">
        <Icon name="search" size={15} className="shrink-0 text-muted" />
        <input
          ref={input}
          role="combobox"
          aria-label="Search commands"
          aria-expanded
          aria-controls={`${id}-list`}
          aria-activedescendant={activeId}
          aria-autocomplete="list"
          autoComplete="off"
          spellCheck={false}
          // The palette's own border shows where the cursor is; the global focus ring would box the input inside it.
          style={{ outline: "none" }}
          value={query}
          placeholder="Type a command or search flows, sensors, chats…"
          className="h-11 min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted"
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
          }}
        />
        <Kbd>esc</Kbd>
      </div>
      <div
        id={`${id}-list`}
        role="listbox"
        aria-label="Commands"
        className="max-h-[60vh] overflow-y-auto p-1.5"
      >
        {result.sections.length === 0 && (
          <div className="px-3 py-6 text-center text-xs text-muted">
            No command matches “{query.trim()}”.
          </div>
        )}
        {result.sections.map((section, si) => (
          <div
            key={section.title}
            role="group"
            aria-label={section.title}
            className="mb-1"
          >
            <div className="px-2 pb-0.5 pt-1.5 text-[10px] font-medium uppercase tracking-wider text-muted">
              {section.title}
            </div>
            {section.items.map((m, k) => {
              const i = (starts[si] ?? 0) + k;
              const c = m.command;
              const isActive = i === index;
              const disabled = c.disabled !== undefined;
              return (
                <div
                  key={c.id}
                  id={optionId(i)}
                  role="option"
                  aria-selected={isActive}
                  aria-disabled={disabled || undefined}
                  title={c.disabled}
                  className={cx(
                    "flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-[13px]",
                    isActive && "bg-raised",
                    disabled && "cursor-not-allowed opacity-60",
                  )}
                  onMouseMove={() => setActive(i)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => run(m)}
                >
                  {c.icon && (
                    <Icon
                      name={c.icon}
                      size={15}
                      className="shrink-0 text-muted"
                    />
                  )}
                  <span className="min-w-0 truncate">
                    <Title text={c.title} indices={m.indices} />
                    {c.subtitle && !disabled && (
                      <span className="ml-2 text-[11px] text-muted">
                        {c.subtitle}
                      </span>
                    )}
                  </span>
                  {disabled ? (
                    <span className="ml-auto shrink-0 text-[11px] text-muted">
                      {c.disabled}
                    </span>
                  ) : isActive && result.arg !== undefined && c.acceptsText ? (
                    <span className="ml-auto max-w-[45%] shrink-0 truncate text-[11px] text-accent">
                      send “{result.arg}”
                    </span>
                  ) : c.shortcut ? (
                    <Shortcut shortcut={c.shortcut} />
                  ) : null}
                </div>
              );
            })}
          </div>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line px-3 py-1.5 text-[11px] text-muted">
        <span>
          <Kbd>↑</Kbd> <Kbd>↓</Kbd> move
        </span>
        <span>
          <Kbd>↵</Kbd> run
        </span>
        <span className="ml-auto">
          Add text after a colon: <code>ask ai: why did it fail</code>
        </span>
      </div>
    </dialog>
  );
}
