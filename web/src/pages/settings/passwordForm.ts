/** The shortest password the server accepts (`PUT /api/auth/password`). */
export const MIN_PASSWORD_LENGTH = 12;

export interface PasswordFormErrors {
  current?: string;
  password?: string;
  repeat?: string;
}

/** Characters, not UTF-16 units, so an emoji counts once. */
function length(s: string): number {
  return [...s].length;
}

/**
 * Client-side checks before a set or change request. `current` is checked only when the form asks for it
 * (a password is already set); the server decides whether it is right. Nothing here is trimmed:
 * spaces are part of a password.
 */
export function passwordFormErrors(form: {
  current: string | null;
  password: string;
  repeat: string;
}): PasswordFormErrors {
  const errors: PasswordFormErrors = {};
  if (form.current !== null && form.current === "") {
    errors.current = "Enter your current password.";
  }
  if (length(form.password) < MIN_PASSWORD_LENGTH) {
    errors.password = `Use at least ${MIN_PASSWORD_LENGTH} characters.`;
  }
  if (form.repeat !== form.password) {
    errors.repeat = "The two passwords do not match.";
  }
  return errors;
}

/** Whether `errors` has any message. */
export function hasErrors(errors: PasswordFormErrors): boolean {
  return Object.keys(errors).length > 0;
}
