package spec

import (
	"maps"
	"reflect"
	"slices"
	"time"

	"hal9001/internal/transform"
)

// DefaultAuth returns the default auth configuration (REQUIREMENTS §10.4):
// one stack used by all four services.
func DefaultAuth() Auth {
	return Auth{
		Stacks: map[string][]AuthModule{
			DefaultStack: {
				{Module: "localhost-trust", Control: ControlSufficient},
				{Module: "tailscale-identity", Control: ControlSufficient},
				{Module: "webauthn", Control: ControlSufficient},
				{Module: "password", Control: ControlSufficient},
			},
		},
		Services: map[string]string{
			ServiceUI: DefaultStack, ServiceAPI: DefaultStack,
			ServiceApprove: DefaultStack, ServiceAdmin: DefaultStack,
		},
	}
}

// ApplyDefaults fills unset fields in place. It is idempotent.
func (c *Config) ApplyDefaults() {
	if c.MaxAgentRuns == 0 {
		c.MaxAgentRuns = DefaultMaxAgentRuns
	}
	if c.MaxCommandRuns == 0 {
		c.MaxCommandRuns = DefaultMaxCommandRuns
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = DefaultRetentionDays
	}
	if c.DedupRetentionDays == 0 {
		c.DedupRetentionDays = DefaultDedupRetentionDays
	}
	if c.NotifyMinPriority == "" {
		c.NotifyMinPriority = DefaultNotifyMinPriority
	}
	if c.ApprovalTimeout == 0 {
		c.ApprovalTimeout = DefaultApprovalTimeout
	}
	if c.SessionTTL == 0 {
		c.SessionTTL = DefaultSessionTTL
	}
	if len(c.Auth.Stacks) == 0 {
		c.Auth.Stacks = DefaultAuth().Stacks
	}
	if c.Auth.Services == nil {
		c.Auth.Services = map[string]string{}
	}
	for _, s := range Services {
		if c.Auth.Services[s] == "" {
			c.Auth.Services[s] = DefaultStack
		}
	}
}

// Clone returns a copy that shares no slices or maps with c at the top two
// levels (runtime and auth module values are shared).
func (c Config) Clone() Config {
	c.AllowedHosts = slices.Clone(c.AllowedHosts)
	c.AllowedLogins = slices.Clone(c.AllowedLogins)
	c.Runtimes = maps.Clone(c.Runtimes)
	c.Auth.Services = maps.Clone(c.Auth.Services)
	if c.Auth.Stacks != nil {
		stacks := make(map[string][]AuthModule, len(c.Auth.Stacks))
		for k, v := range c.Auth.Stacks {
			stacks[k] = slices.Clone(v)
		}
		c.Auth.Stacks = stacks
	}
	return c
}

// Minimal returns a copy with every value that equals its default cleared, so
// that marshaling writes only what the user chose. Applying defaults to the
// result yields the same configuration.
func (c Config) Minimal() Config {
	c = c.Clone()
	if c.MaxAgentRuns == DefaultMaxAgentRuns {
		c.MaxAgentRuns = 0
	}
	if c.MaxCommandRuns == DefaultMaxCommandRuns {
		c.MaxCommandRuns = 0
	}
	if c.RetentionDays == DefaultRetentionDays {
		c.RetentionDays = 0
	}
	if c.DedupRetentionDays == DefaultDedupRetentionDays {
		c.DedupRetentionDays = 0
	}
	if c.NotifyMinPriority == DefaultNotifyMinPriority {
		c.NotifyMinPriority = ""
	}
	if c.ApprovalTimeout == DefaultApprovalTimeout {
		c.ApprovalTimeout = 0
	}
	if c.SessionTTL == DefaultSessionTTL {
		c.SessionTTL = 0
	}
	if reflect.DeepEqual(c.Auth.Stacks, DefaultAuth().Stacks) {
		c.Auth.Stacks = nil
	}
	for s, stack := range c.Auth.Services {
		if stack == DefaultStack {
			delete(c.Auth.Services, s)
		}
	}
	if len(c.Auth.Services) == 0 {
		c.Auth.Services = nil
	}
	return c
}

// ApprovalTimeoutFor returns the approval timeout for agent node n: the
// node's own value, else the config-wide default.
func (c *Config) ApprovalTimeoutFor(n *Node) time.Duration {
	if n != nil && n.ApprovalTimeout > 0 {
		return n.ApprovalTimeout.Std()
	}
	if c.ApprovalTimeout > 0 {
		return c.ApprovalTimeout.Std()
	}
	return DefaultApprovalTimeout.Std()
}

// ApplyDefaults fills unset fields in place. It is idempotent.
func (s *Sensor) ApplyDefaults() {
	if s.Timeout == 0 {
		s.Timeout = DefaultCommandTimeout
	}
	if s.FirstRun == "" {
		s.FirstRun = FirstRunBaseline
	}
}

// Clone returns a copy sharing no slices with s.
func (s Sensor) Clone() Sensor {
	s.Command = slices.Clone(s.Command)
	return s
}

// Minimal returns a copy with default values cleared; see Config.Minimal.
func (s Sensor) Minimal() Sensor {
	s = s.Clone()
	if s.Timeout == DefaultCommandTimeout {
		s.Timeout = 0
	}
	if s.FirstRun == FirstRunBaseline {
		s.FirstRun = ""
	}
	return s
}

// ApplyDefaults fills unset fields in place. It is idempotent. Agent nodes'
// approval_timeout is deliberately left unset: it falls back to the
// config-wide value at run time (Config.ApprovalTimeoutFor).
func (f *Flow) ApplyDefaults() {
	if f.MaxConcurrent == 0 {
		f.MaxConcurrent = DefaultMaxConcurrent
	}
	for i := range f.Nodes {
		n := &f.Nodes[i]
		switch n.Type {
		case NodeCommand:
			if n.Timeout == 0 {
				n.Timeout = DefaultCommandTimeout
			}
			if len(n.Ports) == 0 {
				n.Ports = []string{PortOut}
			}
		case NodeAgent:
			if n.Timeout == 0 {
				n.Timeout = DefaultAgentTimeout
			}
			if n.Mode == ModeApprove && n.Approve == "" {
				n.Approve = ApproveProposal
			}
			for j := range n.MCPServers {
				if n.MCPServers[j].Type == "" {
					n.MCPServers[j].Type = MCPStdio
				}
			}
		case NodeInbox:
			if n.Priority == "" {
				n.Priority = PriorityNormal
			}
		case NodeTransform:
			if n.Timeout == 0 {
				n.Timeout = DefaultCommandTimeout
			}
		}
	}
	for i := range f.Edges {
		if f.Edges[i].Port == "" {
			f.Edges[i].Port = PortOut
		}
	}
}

func (n Node) clone() Node {
	n.Steps = transform.Clone(n.Steps)
	n.Command = slices.Clone(n.Command)
	n.Links = slices.Clone(n.Links)
	n.MCPServers = slices.Clone(n.MCPServers)
	n.Tools = slices.Clone(n.Tools)
	for i, t := range n.Tools {
		n.Tools[i] = t.Clone()
	}
	if n.Cwd != nil {
		c := *n.Cwd
		n.Cwd = &c
	}
	if n.Allow != nil {
		a := Allow{Tools: slices.Clone(n.Allow.Tools)}
		n.Allow = &a
	}
	return n
}

// Clone returns a copy that can be modified without affecting f.
func (f Flow) Clone() Flow {
	f.Subscribe = slices.Clone(f.Subscribe)
	f.Edges = slices.Clone(f.Edges)
	nodes := make([]Node, len(f.Nodes))
	for i, n := range f.Nodes {
		nodes[i] = n.clone()
	}
	f.Nodes = nodes
	return f
}

// Minimal returns a copy with default values cleared; see Config.Minimal.
func (f Flow) Minimal() Flow {
	f = f.Clone()
	if f.MaxConcurrent == DefaultMaxConcurrent {
		f.MaxConcurrent = 0
	}
	for i := range f.Nodes {
		n := &f.Nodes[i]
		switch n.Type {
		case NodeCommand:
			if n.Timeout == DefaultCommandTimeout {
				n.Timeout = 0
			}
			if slices.Equal(n.Ports, []string{PortOut}) {
				n.Ports = nil
			}
		case NodeAgent:
			if n.Timeout == DefaultAgentTimeout {
				n.Timeout = 0
			}
			if n.Mode == ModeApprove && n.Approve == ApproveProposal {
				n.Approve = ""
			}
			for j := range n.MCPServers {
				if n.MCPServers[j].Type == MCPStdio {
					n.MCPServers[j].Type = ""
				}
			}
		case NodeInbox:
			if n.Priority == PriorityNormal {
				n.Priority = ""
			}
		case NodeTransform:
			if n.Timeout == DefaultCommandTimeout {
				n.Timeout = 0
			}
		}
	}
	for i := range f.Edges {
		if f.Edges[i].Port == PortOut {
			f.Edges[i].Port = ""
		}
	}
	return f
}

// NodeByID returns the node with the given id, or nil.
func (f *Flow) NodeByID(id string) *Node {
	for i := range f.Nodes {
		if f.Nodes[i].ID == id {
			return &f.Nodes[i]
		}
	}
	return nil
}

// EntryNodes returns the ids of nodes with no incoming edge, in declaration
// order. Each receives the event (REQUIREMENTS §6).
func (f *Flow) EntryNodes() []string {
	incoming := make(map[string]bool, len(f.Edges))
	for _, e := range f.Edges {
		incoming[e.To] = true
	}
	var out []string
	for _, n := range f.Nodes {
		if !incoming[n.ID] {
			out = append(out, n.ID)
		}
	}
	return out
}

// OutPorts returns every port the node may emit on: the declared ports plus
// the implicit error port, plus denied for agent nodes in approve mode. Call
// it on a node of a flow that has had defaults applied.
func (n *Node) OutPorts() []string {
	var ports []string
	switch n.Type {
	case NodeCommand:
		ports = slices.Clone(n.Ports)
	case NodeTransform:
		ports = transform.Ports(n.Steps)
	default:
		ports = []string{PortOut}
	}
	if n.Type == NodeAgent && n.Mode == ModeApprove {
		ports = append(ports, PortDenied)
	}
	return append(ports, PortError)
}
