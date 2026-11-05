/** Message for an invalid env key name, or undefined when valid (EnvKeyName in the API contract). */
export function envKeyError(key: string): string | undefined {
  if (key === "") return "Enter a name.";
  if (key.length > 128) return "At most 128 characters.";
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) {
    return "Letters, digits, and underscores; not starting with a digit.";
  }
  return undefined;
}
