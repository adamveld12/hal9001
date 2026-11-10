import { useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type McpInfo = Schemas["McpInfo"];

/** How to connect an MCP client to this daemon: URLs, stdio command, auth hints, tools. */
export function useMcpInfo() {
  return useQuery({
    queryKey: qk("mcp-info"),
    queryFn: () => unwrap(api.GET("/api/mcp/info")),
  });
}
