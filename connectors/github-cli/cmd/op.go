package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/github-cli/internal/ops"
)

// OpCommand holds the operations of the Trebi connection. Each one reads
// one JSON object on stdin and writes one JSON object on stdout. An error
// is one JSON line on stderr.
func OpCommand() *cli.Command {
	usage := map[string]string{
		"search":         `Find issues and pull requests: {"query", "limit"? (1-50, default 20)}`,
		"get":            `Show an issue or a pull request: {"repository", "number", "comments"? (0-50, default 10)}`,
		"my_work":        `List review requests, assigned items, and your open pull requests: {"limit"? (1-50, default 20)}`,
		"comment":        `Comment on an issue or a pull request: {"repository", "number", "body"}`,
		"create_issue":   `Open an issue: {"repository", "title", "body"?, "labels"?, "assignees"?}`,
		"review_pr":      `Review a pull request: {"repository", "number", "verdict" (approve|request_changes|comment), "body"?}`,
		"merge_pr":       `Merge a pull request: {"repository", "number", "method"? (merge|squash|rebase, default squash), "delete_branch"?}`,
		"rerun_workflow": `Run a workflow run again: {"repository", "run_id", "failed_only"? (default true)}`,
	}
	var subs []*cli.Command
	for _, name := range ops.Names() {
		subs = append(subs, &cli.Command{
			Name:  name,
			Usage: usage[name],
			Action: func(c *cli.Context) error {
				in, err := io.ReadAll(io.LimitReader(c.App.Reader, 1<<20))
				if err != nil {
					return &ops.Error{Code: ops.ExitInput, Message: "read stdin: " + err.Error()}
				}
				gh, err := clientFromCtx(c)
				if err != nil {
					return &ops.Error{Code: ops.ExitAPI, Message: err.Error()}
				}
				out, err := ops.New(gh).Run(c.Context, name, in)
				if err != nil {
					return err
				}
				return printJSON(c.App.Writer, out)
			},
		})
	}
	return &cli.Command{
		Name:  "op",
		Usage: "Operations for Trebi: JSON in on stdin, JSON out",
		Description: `Each operation reads one JSON object on stdin and writes one JSON object.
search, get, and my_work only read. The others change data on GitHub.

  echo '{"query":"is:open is:pr repo:octo/app"}' | github-cli op search
  echo '{"repository":"octo/app","number":7}' | github-cli op get`,
		Subcommands: subs,
	}
}

// Report writes err to w and returns the exit code. An operation error is
// one JSON line.
func Report(w io.Writer, err error) int {
	var oe *ops.Error
	if errors.As(err, &oe) {
		fmt.Fprintln(w, oe.JSON())
		return oe.Code
	}
	fmt.Fprintf(w, "error: %v\n", err)
	return 1
}
