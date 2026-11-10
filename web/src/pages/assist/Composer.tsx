import { useEffect, useRef, useState } from "react";
import { errorMessage } from "../../api/client";
import { Button, TextArea } from "../../components/ui";
import { focusLost, noFocusOnClick } from "../../lib/focus";

/**
 * Multiline message input: Enter sends, Shift+Enter inserts a newline. The
 * text stays when sending fails. `prefill` (a clicked starter prompt) replaces it.
 */
export function Composer({
  disabled,
  placeholder,
  prefill,
  autoFocus,
  onSend,
}: {
  disabled: boolean;
  placeholder: string;
  prefill: { text: string; n: number } | null;
  /** Focus the input when the composer mounts (the drawer opened, or the conversation started). */
  autoFocus?: boolean;
  onSend: (text: string) => Promise<void>;
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
    if (prefill) ref.current?.focus();
  }, [prefill]);
  useEffect(() => {
    if (autoFocus) ref.current?.focus();
  }, [autoFocus]);
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
    if (t === "" || disabled || sending) return;
    setSending(true);
    setError(null);
    try {
      await onSend(t);
      setText("");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="space-y-1.5">
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
          readOnly={sending}
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
          disabled={disabled || sending || text.trim() === ""}
          {...noFocusOnClick}
          onClick={() => void submit()}
        >
          {sending ? "Sending…" : "Send"}
        </Button>
      </div>
      <div className="text-[11px] text-muted">
        Enter to send, Shift+Enter for a new line
      </div>
    </div>
  );
}
