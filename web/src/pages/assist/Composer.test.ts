// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { carryDraft, takeCarriedDraft } from "./composerDraft";
import { Composer } from "./Composer";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const setValue = Object.getOwnPropertyDescriptor(
  HTMLTextAreaElement.prototype,
  "value",
)?.set;

/** Type into the focused element the way a browser does: a read-only or disabled field takes nothing. */
function type(text: string) {
  const el = document.activeElement;
  if (!(el instanceof HTMLTextAreaElement))
    throw new Error("no text box has focus");
  for (const ch of text) {
    if (el.readOnly || el.disabled) continue;
    act(() => {
      setValue?.call(el, el.value + ch);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }
}

function pressEnter() {
  const el = document.activeElement;
  if (!el) throw new Error("nothing has focus");
  act(() => {
    el.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
  });
}

/** A send that stays in flight until the test settles it. */
function pendingSend() {
  let resolve = () => {};
  let reject: (reason: Error) => void = () => {};
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function mount(onSend: (text: string, draft: () => string) => Promise<void>) {
  act(() =>
    root.render(
      createElement(Composer, {
        disabled: false,
        placeholder: "Message the agent…",
        prefill: null,
        autoFocus: true,
        onSend,
        onNewChat: () => undefined,
        onPrefillApplied: () => undefined,
      }),
    ),
  );
  const box = host.querySelector("textarea");
  if (!box) throw new Error("no message box");
  return box;
}

describe("Composer while a message is being sent", () => {
  it("keeps every character typed right after sending", async () => {
    const send = pendingSend();
    const onSend = vi.fn((_text: string, _draft: () => string) => send.promise);
    const box = mount(onSend);
    expect(document.activeElement).toBe(box);

    type("hello");
    pressEnter();
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend.mock.calls[0]?.[0]).toBe("hello");
    expect(box.value).toBe("");

    type("abc");
    expect(box.value).toBe("abc");

    await act(async () => send.resolve());
    expect(box.value).toBe("abc");
    expect(document.activeElement).toBe(box);
  });

  it("brings the message back, ahead of what was typed meanwhile, when sending fails", async () => {
    const send = pendingSend();
    const box = mount(() => send.promise);

    type("hello");
    pressEnter();
    type("abc");
    await act(async () => send.reject(new Error("offline")));

    expect(box.value).toBe("hello\nabc");
    expect(host.querySelector("[role=alert]")?.textContent).toBe("offline");
  });

  it("returns the message alone when nothing was typed meanwhile and sending fails", async () => {
    const send = pendingSend();
    const box = mount(() => send.promise);

    type("hello");
    pressEnter();
    await act(async () => send.reject(new Error("offline")));

    expect(box.value).toBe("hello");
  });
});

describe("Composer after a message box that went away", () => {
  it("lets the sender read what was typed since the message went out, and opens with it", async () => {
    const send = pendingSend();
    let draft = () => "";
    const box = mount((_text, read) => {
      draft = read;
      return send.promise;
    });

    type("hello");
    pressEnter();
    type("abc");
    expect(draft()).toBe("abc");
    carryDraft(draft());
    act(() => root.unmount());
    await send.resolve();

    root = createRoot(host);
    const next = mount(() => send.promise);
    expect(next).not.toBe(box);
    expect(next.value).toBe("abc");
    expect(document.activeElement).toBe(next);
    expect(takeCarriedDraft()).toBe("");
  });
});
