import { useEffect, useRef, useState } from "react";
import { errorMessage } from "../../api/client";
import { Button, TextArea } from "../../components/ui";
import { focusLost, noFocusOnClick } from "../../lib/focus";
import { afterFailedSend, takeCarriedDraft } from "./composerDraft";
import { parseSlashCommand, slashHints } from "./slashCommands";

/**
 * Multiline message input: Enter sends, Shift+Enter inserts a newline. The
 * message leaves the box when it is sent and comes back if sending fails;
 * typing is never blocked meanwhile. `prefill` (a clicked starter prompt)
 * replaces the text.
 * `/new` and `/clear` start a new chat instead of sending, also while the agent
 * is working.
 */
export function Composer({
  disabled,
  placeholder,
  prefill,
  autoFocus,
  onSend,
  onNewChat,
  onPrefillApplied,
}: {
  disabled: boolean;
  placeholder: string;
  prefill: { text: string; n: number; send?: boolean } | null;
  /** Focus the input when the composer mounts (the drawer opened, or the conversation started). */
  autoFocus?: boolean;
  /** `draft` reads what is in the box now: what was typed since `text` was sent. */
  onSend: (text: string, draft: () => string) => Promise<void>;
  onNewChat: () => void;
  /** Called once the prefill is in the input, so a remount does not apply it again. */
  onPrefillApplied: () => void;
}) {
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);

  // A clicked starter prompt replaces the text (adjusting state while rendering, keyed by its counter).
  const [appliedPrefill, setAppliedPrefill] = useState(0);
  if (prefill && prefill.n !== appliedPrefill) {
    setAppliedPrefill(prefill.n);
    setText(prefill.text);
  }
  useEffect(() => {
    if (!prefill) return;
    ref.current?.focus();
    // A prefill that asks to be sent stays until it is (see below), so a
    // composer that remounts first still sends it.
    if (prefill.send !== true) onPrefillApplied();
  }, [prefill, onPrefillApplied]);
  useEffect(() => {
    if (autoFocus) ref.current?.focus();
  }, [autoFocus]);
  // Text typed into the start view's box while its first message was on its
  // way. Read from an effect, not during render: taking it is a side effect
  // that a discarded render must not repeat.
  useEffect(() => {
    const carried = takeCarriedDraft();
    // eslint-disable-next-line react/set-state-in-effect
    if (carried !== "") setText(carried);
  }, []);
  // When the agent finishes (disabled -> enabled), take focus back if it fell
  // to the page (an approval or Stop button that held it went away). Focus the
  // user moved to another control, such as the editor, is left alone.
  const wasDisabled = useRef(disabled);
  useEffect(() => {
    if (
      wasDisabled.current &&
      !disabled &&
      focusLost(document.activeElement, document.body)
    ) {
      ref.current?.focus();
    }
    wasDisabled.current = disabled;
  }, [disabled]);

  const submit = async () => {
    const t = text.trim();
    if (t === "" || sending) return;
    if (parseSlashCommand(t) !== null) {
      setText("");
      onNewChat();
      return;
    }
    if (disabled) return;
    setSending(true);
    setError(null);
    // The message leaves the box now, so typing the next one is never held up
    // by the request. It comes back if sending fails.
    setText("");
    try {
      await onSend(t, () => ref.current?.value ?? "");
    } catch (e) {
      setText((typed) => afterFailedSend(text, typed));
      setError(errorMessage(e));
    } finally {
      setSending(false);
    }
  };

  useEffect(() => {
    if (
      prefill?.send !== true ||
      prefill.n !== appliedPrefill ||
      disabled ||
      sending
    ) {
      return;
    }
    onPrefillApplied();
    // Not inline: submit sets state, which an effect should not do synchronously.
    queueMicrotask(() => void submit());
  });

  const hints = slashHints(text);
  const isCommand = parseSlashCommand(text) !== null;

  return (
    <div className="space-y-1.5">
      {hints.length > 0 && (
        <ul
          aria-label="Commands"
          className="m-0 list-none space-y-0.5 rounded-md border border-line bg-panel p-1.5 text-[11px] text-muted"
        >
          {hints.map((c) => (
            <li key={c.name}>
              <code className="text-fg">{c.name}</code> {c.description}
            </li>
          ))}
        </ul>
      )}
      {error && (
        <div role="alert" className="text-xs text-bad">
          {error}
        </div>
      )}
      <div className="flex items-end gap-2">
        <TextArea
          ref={ref}
          aria-label="Message to the agent"
          rows={3}
          className="min-w-0 flex-1 resize-none font-sans text-[13px]"
          placeholder={placeholder}
          value={text}
          aria-disabled={disabled || undefined}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing
            ) {
              e.preventDefault();
              void submit();
            }
          }}
        />
        <Button
          variant="primary"
          disabled={(disabled && !isCommand) || sending || text.trim() === ""}
          {...noFocusOnClick}
          onClick={() => void submit()}
        >
          {sending ? "Sending…" : "Send"}
        </Button>
      </div>
      <div className="text-[11px] text-muted">
        Enter to send, Shift+Enter for a new line, <code>/new</code> for a new
        chat
      </div>
    </div>
  );
}
