const RUNTIME_STORAGE_ITEM = "hal9001.assist.runtime";

/** The runtime of the last chat the user started, so a new chat does not reset it to the first one. */
export function storedRuntime(): string | null {
  try {
    return localStorage.getItem(RUNTIME_STORAGE_ITEM);
  } catch {
    return null;
  }
}

export function saveRuntime(name: string) {
  try {
    localStorage.setItem(RUNTIME_STORAGE_ITEM, name);
  } catch {
    // not persisted
  }
}
