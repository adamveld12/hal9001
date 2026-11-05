import type { CodeLang } from "../components/CodeEditor";

/** CodeMirror language for a command node / sensor `lang`. */
export function langForCommand(lang: string): CodeLang {
  switch (lang) {
    case "bash":
      return "shell";
    case "python":
      return "python";
    case "node":
      return "javascript";
    default:
      return "text";
  }
}
