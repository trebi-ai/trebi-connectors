package cmd

import (
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/spec"
)

var nounAliases = map[string][]string{
	"work-item":      {"wi", "issue"},
	"work-item-type": {"type"},
	"project":        {"proj"},
	"property":       {"prop"},
}

// node is a noun in the command tree.
type node struct {
	name     string
	usage    string
	children []*node
	leaf     *cli.Command
}

func (n *node) child(name string) *node {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	c := &node{name: name}
	n.children = append(n.children, c)
	return c
}

func (n *node) command() *cli.Command {
	if n.leaf != nil {
		return n.leaf
	}
	cmd := &cli.Command{Name: n.name, Usage: n.usage, Aliases: nounAliases[n.name]}
	for _, c := range n.children {
		cmd.Commands = append(cmd.Commands, c.command())
	}
	return cmd
}

// ResourceCommands builds one command tree per noun from the spec. extra adds
// hand-written leaves at a noun path (for example the upload helpers).
func ResourceCommands(s *spec.Spec, extra map[string][]*cli.Command) []*cli.Command {
	root := &node{}
	for _, sc := range s.Commands {
		n := root
		for i, part := range sc.Path {
			n = n.child(part)
			if i == 0 {
				n.usage = s.Nouns[part]
			} else if n.usage == "" && i < len(sc.Path)-1 {
				n.usage = strings.Join(sc.Path[:i+1], " ") + " commands"
			}
		}
		n.leaf = newEndpointCommand(sc).Command()
	}
	for path, cmds := range extra {
		n := root
		for _, part := range strings.Fields(path) {
			n = n.child(part)
		}
		for _, c := range cmds {
			n.children = append(n.children, &node{name: c.Name, leaf: c})
		}
	}
	var out []*cli.Command
	for _, c := range root.children {
		out = append(out, c.command())
	}
	return out
}
