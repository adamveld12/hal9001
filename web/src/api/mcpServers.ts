import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";

export type RemoteMcpServer = Schemas["RemoteMcpServer"];
export type RemoteMcpServerDetail = Schemas["RemoteMcpServerDetail"];
export type RemoteMcpTool = Schemas["RemoteMcpTool"];
export type RemoteMcpTestResult = Schemas["RemoteMcpTestResult"];
export type RemoteMcpToolsRefresh = Schemas["RemoteMcpToolsRefresh"];
export type RemoteMcpServerCreateRequest =
  Schemas["RemoteMcpServerCreateRequest"];
export type RemoteMcpServerUpdateRequest =
  Schemas["RemoteMcpServerUpdateRequest"];

/** Configured remote MCP servers with status. Refetched on `mcp_server.updated`. */
export function useMcpServers() {
  return useQuery({
    queryKey: qk("mcp-servers"),
    queryFn: () => unwrap(api.GET("/api/mcp-servers")),
    select: (d) => d.items,
  });
}

/** One server with its cached tool list. */
export function useMcpServer(name: string, opts?: { enabled?: boolean }) {
  return useQuery({
    enabled: opts?.enabled ?? true,
    queryKey: qk("mcp-servers", name),
    queryFn: () =>
      unwrap(
        api.GET("/api/mcp-servers/{name}", { params: { path: { name } } }),
      ),
  });
}

function useInvalidateServers() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: qk("mcp-servers") });
}

export function useCreateMcpServer() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: (body: RemoteMcpServerCreateRequest) =>
      unwrap(api.POST("/api/mcp-servers", { body })),
    onSuccess: invalidate,
  });
}

export function useUpdateMcpServer(name: string) {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: (body: RemoteMcpServerUpdateRequest) =>
      unwrap(
        api.PUT("/api/mcp-servers/{name}", {
          params: { path: { name } },
          body,
        }),
      ),
    onSuccess: invalidate,
  });
}

/** Rejects with ApiError `conflict` (409) naming the sensors that still use the server. */
export function useDeleteMcpServer() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: (name: string) =>
      unwrapVoid(
        api.DELETE("/api/mcp-servers/{name}", { params: { path: { name } } }),
      ),
    onSuccess: invalidate,
  });
}

/** Resolves with the URL to open in a browser; 422/502 problems reject. */
export function useConnectMcpServer() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.POST("/api/mcp-servers/{name}/connect", {
          params: { path: { name } },
        }),
      ),
    onSettled: invalidate,
  });
}

export function useDisconnectMcpServer() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.POST("/api/mcp-servers/{name}/disconnect", {
          params: { path: { name } },
        }),
      ),
    onSuccess: invalidate,
  });
}

/** A failed test resolves with `ok: false`. */
export function useTestMcpServer() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.POST("/api/mcp-servers/{name}/test", {
          params: { path: { name } },
        }),
      ),
    onSettled: invalidate,
  });
}

/** 409 when not connected, 502 when unreachable. */
export function useRefreshMcpTools() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.POST("/api/mcp-servers/{name}/tools/refresh", {
          params: { path: { name } },
        }),
      ),
    onSettled: invalidate,
  });
}
