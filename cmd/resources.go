package cmd

import (
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/flarco/cli-tools/slack-cli/internal/spec"
)

// nounAliases are short names for the top-level groups.
var nounAliases = map[string][]string{
	"conversations": {"conv", "channel", "channels"},
	"chat":          {"msg", "message"},
	"users":         {"user"},
	"usergroups":    {"usergroup"},
	"reactions":     {"react", "reaction"},
}

// node is a group in the command tree.
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
// hand-written leaves at a group path (space-separated).
func ResourceCommands(s *spec.Spec, extra map[string][]*cli.Command) []*cli.Command {
	root := &node{}
	for _, m := range s.Methods {
		n := root
		for i, part := range m.Path {
			n = n.child(part)
			if i == 0 {
				n.usage = s.Nouns[part]
			} else if n.usage == "" && i < len(m.Path)-1 {
				n.usage = strings.Join(m.Path[:i+1], " ") + " methods"
			}
		}
		n.leaf = newMethodCommand(m).Command()
	}
	for path, cmds := range extra {
		n := root
		for _, part := range strings.Fields(path) {
			n = n.child(part)
		}
		for _, c := range cmds {
			child := n.child(c.Name) // replaces a same-named spec leaf
			child.leaf = c
		}
	}
	var out []*cli.Command
	for _, c := range root.children {
		out = append(out, c.command())
	}
	return out
}
