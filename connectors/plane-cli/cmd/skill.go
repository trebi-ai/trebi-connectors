package cmd

import (
	"context"
	"fmt"
	"io"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/skill"
)

// SkillCommand returns the `skill` command. It prints the reference and needs
// no API key, no host, and no config.
func SkillCommand() *cli.Command {
	return &cli.Command{
		Name:  "skill",
		Usage: "Print the plane-cli reference as Markdown. It needs no API key",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "raw", Usage: "Print SKILL.md as it is, with the frontmatter and the markers"},
		},
		Action: runSkill,
	}
}

func runSkill(ctx context.Context, c *cli.Command) error {
	w := c.Root().Writer
	if c.Bool("raw") {
		_, err := w.Write(skill.Raw())
		return err
	}
	text, err := skill.Render(config.Managed())
	if err != nil {
		return fmt.Errorf("skill text: %w", err)
	}
	_, err = io.WriteString(w, text)
	return err
}
