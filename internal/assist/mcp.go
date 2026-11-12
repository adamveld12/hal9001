package assist

import "hal9001/internal/spec"

// sensorRefs are the references a sensor draft may use: the remote MCP servers of config.yaml (its mcp_servers).
func (m *Manager) sensorRefs() *spec.Refs { return spec.SensorRefs(m.home) }
