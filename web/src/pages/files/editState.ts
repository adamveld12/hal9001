// State of editing one hash-guarded file (sensor YAML, prompt markdown).
// `base` is the last version read from or written to the server, `draft` the
// text in the editor. Saves send `base.hash` as `expected_hash`.

export interface FileBase {
  hash: string;
  text: string;
}

export interface Conflict {
  /** `save`: the server rejected a write (409). `remote`: a newer version arrived while the draft had edits. */
  kind: "save" | "remote";
  /** Hash currently on disk; null when unknown or the file is gone. */
  currentHash: string | null;
}

export interface EditState {
  base: FileBase;
  draft: string;
  conflict: Conflict | null;
}

export type EditAction =
  | { type: "edit"; draft: string }
  /** A save succeeded; `submitted` is the text that was sent, `file` what the server returned. */
  | { type: "saved"; submitted: string; file: FileBase }
  /** The server answered 409 `conflict` to a save or delete. */
  | { type: "conflict"; currentHash: string | null }
  /** A fresh read of the file arrived (refetch, stream event). */
  | { type: "server"; file: FileBase }
  /** Discard the draft and take the server version. */
  | { type: "reload"; file: FileBase }
  /** Keep the draft and take the server's hash, so the next save overwrites. */
  | { type: "rebase"; file: FileBase };

export function initEdit(file: FileBase): EditState {
  return { base: file, draft: file.text, conflict: null };
}

export function isDirty(s: EditState): boolean {
  return s.draft !== s.base.text;
}

export function reduceEdit(s: EditState, a: EditAction): EditState {
  switch (a.type) {
    case "edit":
      return a.draft === s.draft ? s : { ...s, draft: a.draft };
    case "saved":
      return {
        base: a.file,
        // Keep what the user typed while the save was in flight.
        draft: s.draft === a.submitted ? a.file.text : s.draft,
        conflict: null,
      };
    case "conflict":
      return { ...s, conflict: { kind: "save", currentHash: a.currentHash } };
    case "server": {
      if (a.file.hash === s.base.hash) return s;
      if (!isDirty(s)) return initEdit(a.file);
      if (s.conflict?.currentHash === a.file.hash) return s;
      return {
        ...s,
        conflict: { kind: "remote", currentHash: a.file.hash },
      };
    }
    case "reload":
      return initEdit(a.file);
    case "rebase":
      return { base: a.file, draft: s.draft, conflict: null };
  }
}
