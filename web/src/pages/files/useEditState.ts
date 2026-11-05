import { useEffect, useReducer, useState } from "react";
import {
  initEdit,
  isDirty,
  reduceEdit,
  type EditAction,
  type EditState,
  type FileBase,
} from "./editState";

/**
 * Draft state for a file read from the server. Fresh reads (`file` changes
 * after a refetch or stream event) are merged by the reducer: adopted when
 * the draft is clean, flagged as a conflict when it has edits. While dirty it
 * also asks the browser to confirm closing the tab.
 */
export function useEditState(file: FileBase): {
  state: EditState;
  dispatch: (a: EditAction) => void;
  dirty: boolean;
} {
  const [state, dispatch] = useReducer(reduceEdit, file, initEdit);
  const { hash, text } = file;
  useEffect(() => {
    dispatch({ type: "server", file: { hash, text } });
  }, [hash, text]);
  const dirty = isDirty(state);
  useEffect(() => {
    if (!dirty) return;
    const onBeforeUnload = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [dirty]);
  return { state, dispatch, dirty };
}

/** `value`, updated after it has been stable for `ms`. */
export function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return debounced;
}
