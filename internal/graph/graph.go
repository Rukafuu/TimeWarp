package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

type Node struct {
	Event    protocol.Event
	Children []*Node
}
type Diagnostic struct{ Code, EventID, Detail string }
type CausalGraph struct {
	TraceID     string
	Roots       []*Node
	Diagnostics []Diagnostic
}

func Build(events []protocol.Event) CausalGraph {
	g := CausalGraph{}
	nodes := map[string]*Node{}
	duplicates := map[string]bool{}
	for _, e := range events {
		if g.TraceID == "" {
			g.TraceID = e.TraceID
		}
		if _, ok := nodes[e.EventID]; ok {
			duplicates[e.EventID] = true
			continue
		}
		nodes[e.EventID] = &Node{Event: e}
	}
	for id := range duplicates {
		g.Diagnostics = append(g.Diagnostics, Diagnostic{"DUPLICATE", id, "duplicate event id"})
	}
	for _, n := range nodes {
		if n.Event.ParentID == "" {
			g.Roots = append(g.Roots, n)
			continue
		}
		p, ok := nodes[n.Event.ParentID]
		if !ok {
			g.Roots = append(g.Roots, n)
			g.Diagnostics = append(g.Diagnostics, Diagnostic{"ORPHAN", n.Event.EventID, "parent " + n.Event.ParentID + " not found"})
			continue
		}
		p.Children = append(p.Children, n)
	}
	state := map[string]uint8{}
	var visit func(*Node)
	visit = func(n *Node) {
		if state[n.Event.EventID] == 1 {
			g.Diagnostics = append(g.Diagnostics, Diagnostic{"CYCLE", n.Event.EventID, "causal loop detected"})
			return
		}
		if state[n.Event.EventID] == 2 {
			return
		}
		state[n.Event.EventID] = 1
		for _, c := range n.Children {
			visit(c)
		}
		state[n.Event.EventID] = 2
	}
	for _, n := range nodes {
		visit(n)
	}
	sortNodes := func(ns []*Node) {
		sort.Slice(ns, func(i, j int) bool {
			if ns[i].Event.Timestamp == ns[j].Event.Timestamp {
				return ns[i].Event.EventID < ns[j].Event.EventID
			}
			return ns[i].Event.Timestamp < ns[j].Event.Timestamp
		})
	}
	sortNodes(g.Roots)
	for _, n := range nodes {
		sortNodes(n.Children)
		for _, c := range n.Children {
			if c.Event.Timestamp < n.Event.Timestamp {
				g.Diagnostics = append(g.Diagnostics, Diagnostic{"INCONSISTENT_ORDER", c.Event.EventID, "child precedes parent"})
			}
		}
	}
	return g
}
func ASCII(g CausalGraph) string {
	var b strings.Builder
	var walk func(*Node, string, bool)
	walk = func(n *Node, prefix string, last bool) {
		branch := "├── "
		next := prefix + "│   "
		if last {
			branch = "└── "
			next = prefix + "    "
		}
		if prefix == "" {
			branch = ""
		}
		fmt.Fprintf(&b, "%s%s%s [%dms]", prefix, branch, n.Event.Service, n.Event.DurationMS)
		if n.Event.Type == protocol.Error || n.Event.Type == protocol.Timeout {
			fmt.Fprintf(&b, " [%s]", n.Event.Type)
		}
		b.WriteByte('\n')
		for i, c := range n.Children {
			walk(c, next, i == len(n.Children)-1)
		}
	}
	for i, r := range g.Roots {
		walk(r, "", i == len(g.Roots)-1)
	}
	return b.String()
}
