# hal9001 web UI

React 19 + TypeScript + Vite + Tailwind v4. A pure client of the daemon's REST API (`../api/openapi.yaml`); the built assets in `dist/` are embedded into the Go binary.

```sh
mise exec -- pnpm install       # once (or: mise run setup at the repo root)
mise exec -- pnpm dev           # http://localhost:5173, proxies /api to 127.0.0.1:9001
mise exec -- pnpm check         # prettier, oxlint, tsc, vitest
mise exec -- pnpm build         # type-check and emit dist/
mise exec -- pnpm gen:api       # regenerate src/api/schema.d.ts from ../api/openapi.yaml
```

## Layout

- `src/api/`: `schema.d.ts` (generated, committed), `client.ts` (openapi-fetch + `unwrap`/`ApiError`), per-area react-query hooks, `stream.tsx` (SSE provider). Every query key starts with a root from `keys.ts`; `invalidate.ts` maps stream events to the roots they refetch.
- `src/flow/`: the flow editor. `model.ts` is the JSON form of the flow YAML; `graph.ts` converts it to and from the React Flow graph; `validate.ts` does instant client-side checks (cycles, ports); `editorState.ts` is the undoable reducer; `FlowCanvas.tsx` is shared by the editor and the read-only run overlay (`overlay.ts`, `runOverlay.ts`).
- `src/pages/assist/`: AI-assisted sensor authoring. The drawer (`AssistDrawer.tsx`) is route-addressable as `/sensors[/<id>]?assist=<sessionId>` (`assist=new` before a session exists; the session is created with the first message). `draftSync.ts` holds the pure decisions that keep the sensor editor and the session draft in step (agent updates replace the editor unless the user typed since; pushes use the draft hash), `messages.ts` merges live `assist.message` events, `useDraftSync.ts` applies them in the editor. Closing the drawer (Esc or ×) keeps the session; "End session" deletes it. Its width is saved in localStorage.
- `src/lib/sensorItems.ts` and `src/components/SensorItemsTable.tsx`: sensor output as a table (stdout parsed like the daemon's `ParseSensorItems`; used by recent polls, Poll now and the assist test result).
- `src/pages/`: one directory per area of REQUIREMENTS §11. `src/layout/`: shell, header, kill switch. `src/components/`: shared UI kit.
- `tools/gen-api/`: a separate workspace package that carries TypeScript 5 for `openapi-typescript`, which needs the compiler JS API that TypeScript 7 does not ship.

Keyboard: `g` then `t i a f s p ,` jumps to a page; inbox and approvals have `j`/`k` and action keys; `⌘S` saves in the flow editor, `⌘Z` / `⇧⌘Z` undo and redo.
