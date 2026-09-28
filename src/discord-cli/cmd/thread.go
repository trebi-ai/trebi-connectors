package cmd

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/src/discord-cli/internal/client"
)

// ThreadCommand returns the `thread` subcommand tree.
func ThreadCommand() *cli.Command {
	return &cli.Command{
		Name:  "thread",
		Usage: "Create, list, manage threads",
		Subcommands: []*cli.Command{
			{
				Name:      "create",
				Usage:     "Create a thread from a message",
				ArgsUsage: "<channel_id> <message_id> <name>",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "auto-archive", Value: 1440, Usage: "auto-archive duration in minutes (60, 1440, 4320, 10080)"},
				},
				Action: threadCreate,
			},
			{
				Name:      "list",
				Usage:     "List threads in a channel or guild",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "archived", Usage: "list archived threads (id must be channel_id)"},
					&cli.BoolFlag{Name: "guild", Usage: "id is a guild_id (list active threads)"},
				},
				Action: threadList,
			},
			{
				Name:      "send",
				Usage:     "Send a message to a thread",
				ArgsUsage: "<thread_id> <text>",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "file", Aliases: []string{"f"}, Usage: "file path to attach"},
				},
				Action: threadSend,
			},
			{
				Name:      "archive",
				Usage:     "Archive a thread",
				ArgsUsage: "<thread_id>",
				Action:    threadArchive,
			},
			{
				Name:      "unarchive",
				Usage:     "Unarchive a thread",
				ArgsUsage: "<thread_id>",
				Action:    threadUnarchive,
			},
			{
				Name:      "rename",
				Usage:     "Rename a thread",
				ArgsUsage: "<thread_id> <new_name>",
				Action:    threadRename,
			},
			{
				Name:      "add-member",
				Usage:     "Add a member to a thread",
				ArgsUsage: "<thread_id> <user_id>",
				Action:    threadAddMember,
			},
			{
				Name:      "remove-member",
				Usage:     "Remove a member from a thread",
				ArgsUsage: "<thread_id> <user_id>",
				Action:    threadRemoveMember,
			},
		},
	}
}

func threadCreate(c *cli.Context) error {
	if c.NArg() < 3 {
		return fmt.Errorf("usage: discord-cli thread create <channel_id> <message_id> <name>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	channelID := c.Args().Get(0)
	messageID := c.Args().Get(1)
	name := c.Args().Get(2)

	body := map[string]any{
		"name":                  name,
		"auto_archive_duration": c.Int("auto-archive"),
	}
	path := fmt.Sprintf("/channels/%s/messages/%s/threads", channelID, messageID)
	var ch client.Channel
	if err := cl.DoJSON(c.Context, "POST", path, body, &ch); err != nil {
		return err
	}
	if outputJSON(c) {
		printJSON(os.Stdout, ch)
	} else {
		fmt.Fprintf(os.Stdout, "Created thread %q (ID: %s)\n", ch.Name, ch.ID)
	}
	return nil
}

func threadList(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli thread list <id> [--guild] [--archived]")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	id := c.Args().Get(0)

	var threads []client.Channel

	if c.Bool("archived") {
		path := fmt.Sprintf("/channels/%s/threads/archived/public", id)
		var resp client.ArchivedThreadsResponse
		if err := cl.DoJSON(c.Context, "GET", path, nil, &resp); err != nil {
			return err
		}
		threads = resp.Threads
	} else {
		path := fmt.Sprintf("/guilds/%s/threads/active", id)
		var resp client.ActiveThreadsResponse
		if err := cl.DoJSON(c.Context, "GET", path, nil, &resp); err != nil {
			return err
		}
		threads = resp.Threads
	}

	if outputJSON(c) {
		printJSON(os.Stdout, threads)
	} else {
		if len(threads) == 0 {
			fmt.Fprintln(os.Stdout, "No threads found")
			return nil
		}
		headers := []string{"ID", "Name", "Archived", "Parent"}
		var rows [][]string
		for _, t := range threads {
			archived := "no"
			if t.ThreadMetadata != nil && t.ThreadMetadata.Archived {
				archived = "yes"
			}
			rows = append(rows, []string{t.ID, t.Name, archived, t.ParentID})
		}
		printTable(os.Stdout, headers, rows)
	}
	return nil
}

func threadSend(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli thread send <thread_id> <text>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	threadID := c.Args().Get(0)
	text := c.Args().Get(1)

	files := c.StringSlice("file")
	if len(files) > 0 {
		return messageSendWithFiles(c, cl, threadID, text, files)
	}

	body := map[string]any{"content": text}
	var msg client.Message
	if err := cl.DoJSON(c.Context, "POST", "/channels/"+threadID+"/messages", body, &msg); err != nil {
		return err
	}
	printMessageOutput(c, &msg)
	return nil
}

func threadArchive(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli thread archive <thread_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	body := map[string]any{"archived": true}
	var ch client.Channel
	if err := cl.DoJSON(c.Context, "PATCH", "/channels/"+c.Args().Get(0), body, &ch); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Archived thread %s\n", ch.ID)
	return nil
}

func threadUnarchive(c *cli.Context) error {
	if c.NArg() < 1 {
		return fmt.Errorf("usage: discord-cli thread unarchive <thread_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	body := map[string]any{"archived": false}
	var ch client.Channel
	if err := cl.DoJSON(c.Context, "PATCH", "/channels/"+c.Args().Get(0), body, &ch); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Unarchived thread %s\n", ch.ID)
	return nil
}

func threadRename(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli thread rename <thread_id> <new_name>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	body := map[string]any{"name": c.Args().Get(1)}
	var ch client.Channel
	if err := cl.DoJSON(c.Context, "PATCH", "/channels/"+c.Args().Get(0), body, &ch); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Renamed thread to %q\n", ch.Name)
	return nil
}

func threadAddMember(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli thread add-member <thread_id> <user_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/channels/%s/thread-members/%s", c.Args().Get(0), c.Args().Get(1))
	if err := cl.DoNoBody(c.Context, "PUT", path); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Member added to thread")
	return nil
}

func threadRemoveMember(c *cli.Context) error {
	if c.NArg() < 2 {
		return fmt.Errorf("usage: discord-cli thread remove-member <thread_id> <user_id>")
	}
	cl, err := clientFromCtx(c)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/channels/%s/thread-members/%s", c.Args().Get(0), c.Args().Get(1))
	if err := cl.DoNoBody(c.Context, "DELETE", path); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Member removed from thread")
	return nil
}
