import { useLocation, useNavigate } from "react-router";
import { errorMessage } from "../../api/client";
import { usePauseSensor, useResumeSensor, useSensors } from "../../api/sensors";
import {
  usePauseKillSwitch,
  useResumeKillSwitch,
  useStatus,
} from "../../api/status";
import { useCheckUpdate, useUpdateStatus } from "../../api/update";
import { isDark, setTheme, useTheme } from "../../lib/theme";
import { useAssist } from "../../pages/assist/context";
import { chatPath, isChatPath, NEW_CHAT_ID } from "../../pages/chat/path";
import { NAV_SECTIONS, navCommands } from "../nav";
import {
  chatCommands,
  createCommands,
  killSwitchCommands,
  plural,
  sensorBulkCommands,
  shellActionCommands,
  updateCheckMessage,
} from "./appCommands";
import { useCommands, useRegisterCommands } from "./context";

const COMPOSER = "[data-chat-composer] textarea";

/** Focus the message box once the palette has given focus back (it closes first). */
function focusComposerSoon() {
  setTimeout(() => {
    const input = document.querySelector<HTMLTextAreaElement>(COMPOSER);
    if (input && !input.disabled) input.focus();
  }, 0);
}

/**
 * Registers the sidebar pages, the create/chat/action commands and the
 * sidebar toggle. Returns `askAi` for the header button, which does what the
 * Ask AI command does.
 */
export function useAppCommands(sidebar: {
  collapsed: boolean;
  toggle: () => void;
}): { askAi: (opts: { text?: string; shortcut: boolean }) => void } {
  const navigate = useNavigate();
  const location = useLocation();
  const assist = useAssist();
  const { notify } = useCommands();
  const status = useStatus();
  const sensors = useSensors();
  const update = useUpdateStatus();
  const check = useCheckUpdate();
  const pauseKill = usePauseKillSwitch();
  const resumeKill = useResumeKillSwitch();
  const pauseSensor = usePauseSensor();
  const resumeSensor = useResumeSensor();
  const theme = useTheme();
  const onChatPage = isChatPath(location.pathname);
  const go = (path: string) => void navigate(path);

  // On the Chat page the drawer is not shown (one chat at a time): Ask AI
  // moves to that page's message box, or starts a new chat when it has none.
  const askAi = ({ text, shortcut }: { text?: string; shortcut: boolean }) => {
    if (onChatPage) {
      const input = document.querySelector<HTMLTextAreaElement>(COMPOSER);
      const usable = input !== null && !input.disabled;
      if (text !== undefined) {
        assist.seedText(text, true);
        if (!usable) go(chatPath(NEW_CHAT_ID));
      } else if (usable) input.focus();
      else go(chatPath(NEW_CHAT_ID));
      return;
    }
    if (text !== undefined) {
      assist.openDrawer();
      assist.seedText(text, true);
    } else if (shortcut) {
      assist.toggle();
    } else {
      assist.openDrawer();
      focusComposerSoon();
    }
  };

  const draftWithAi = (kind: "flow" | "sensor", text: string | undefined) => {
    assist.openTarget({ kind, id: null }, text, text !== undefined);
    const page = kind === "flow" ? "/flows" : "/sensors";
    if (location.pathname + location.search !== page) go(page);
    if (text === undefined) focusComposerSoon();
  };

  const bulk = async (
    ids: string[],
    one: (id: string) => Promise<unknown>,
    done: string,
  ) => {
    const results = await Promise.allSettled(ids.map(one));
    const failed = results.filter((r) => r.status === "rejected");
    const first = failed[0];
    if (first && first.status === "rejected") {
      notify(
        `${done} ${plural(ids.length - failed.length, "sensor")}; ${failed.length} failed: ${errorMessage(first.reason)}`,
        "error",
      );
    } else {
      notify(`${done} ${plural(ids.length, "sensor")}`);
    }
  };

  useRegisterCommands("nav", navCommands(NAV_SECTIONS, go));
  useRegisterCommands("create", createCommands({ go, draftWithAi }));
  useRegisterCommands(
    "chat",
    chatCommands({
      onChatPage,
      askAi,
      newChat: () => {
        if (onChatPage) go(chatPath(NEW_CHAT_ID));
        else {
          assist.newChat();
          assist.openDrawer();
          focusComposerSoon();
        }
      },
      openChatPage: assist.openFullPage,
    }),
  );
  useRegisterCommands("actions", [
    ...shellActionCommands({
      collapsed: sidebar.collapsed,
      dark: isDark(theme),
      toggleSidebar: sidebar.toggle,
      toggleTheme: () => setTheme(isDark(theme) ? "light" : "dark"),
      update: update.data,
      checkForUpdates: () =>
        check.mutate(undefined, {
          onSuccess: (s) => {
            const m = updateCheckMessage(s);
            notify(m.text, m.tone);
          },
          onError: (e) =>
            notify(`Update check failed: ${errorMessage(e)}`, "error"),
        }),
    }),
    ...killSwitchCommands(status.data, {
      stopRunning: () =>
        pauseKill.mutate(
          { kill_running: true },
          {
            onSuccess: () =>
              notify("Stopped running flows. The kill switch is engaged."),
            onError: (e) =>
              notify(`Could not stop flows: ${errorMessage(e)}`, "error"),
          },
        ),
      release: () =>
        resumeKill.mutate(undefined, {
          onSuccess: () => notify("Kill switch released"),
          onError: (e) =>
            notify(`Could not resume: ${errorMessage(e)}`, "error"),
        }),
    }),
    ...sensorBulkCommands(sensors.data?.items, {
      pause: (ids) => void bulk(ids, pauseSensor.mutateAsync, "Paused"),
      resume: (ids) => void bulk(ids, resumeSensor.mutateAsync, "Resumed"),
    }),
  ]);

  return { askAi };
}
