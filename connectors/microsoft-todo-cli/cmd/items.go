package cmd

import (
	"fmt"
	"mime"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/microsoft-todo-cli/internal/client"
)

// taskRun is a command action on one task of a list.
func taskRun(fn func(s *session, t client.Ref) error) cli.ActionFunc {
	return func(c *cli.Context) error {
		s, err := newSession(c)
		if err != nil {
			return err
		}
		t, err := s.task()
		if err != nil {
			return err
		}
		return fn(s, t)
	}
}

// ChecklistCommand returns the `checklist` command: the steps of a task.
func ChecklistCommand() *cli.Command {
	nameFlag := &cli.StringFlag{Name: "name", Usage: "step text"}
	checkedFlag := &cli.BoolFlag{Name: "checked", Usage: "mark the step as done"}
	body := func(c *cli.Context) (map[string]any, error) {
		b, err := readData(c.String("data"))
		if b == nil {
			b = map[string]any{}
		}
		if c.IsSet("name") {
			b["displayName"] = c.String("name")
		}
		if c.IsSet("checked") {
			b["isChecked"] = c.Bool("checked")
		}
		return b, err
	}
	dataFlag := &cli.StringFlag{Name: "data", Usage: "more checklistItem fields as JSON, or @file"}
	check := func(done bool, verb string) cli.ActionFunc {
		return taskRun(func(s *session, t client.Ref) error {
			it, err := s.cl.UpdateChecklistItem(s.c.Context, t, s.c.String("id"), map[string]bool{"isChecked": done})
			if err != nil {
				return err
			}
			return s.done(it, "%s the step %s", verb, it.DisplayName)
		})
	}
	return &cli.Command{
		Name:  "checklist",
		Usage: "Work with the steps (checklist items) of a task",
		Subcommands: []*cli.Command{
			leaf(&cli.Command{
				Name: "list", Usage: "Show the steps of a task",
				Flags: []cli.Flag{listFlag, taskFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					items, err := s.cl.ChecklistItems(s.c.Context, t)
					if err != nil {
						return err
					}
					return s.renderSteps(items)
				}),
			}),
			leaf(&cli.Command{
				Name: "get", Usage: "Show one step",
				Flags: []cli.Flag{listFlag, taskFlag, idFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					it, err := s.cl.ChecklistItem(s.c.Context, t, s.c.String("id"))
					if err != nil {
						return err
					}
					return s.one(it, func() error { return s.renderSteps([]client.ChecklistItem{it}) })
				}),
			}),
			leaf(&cli.Command{
				Name: "create", Usage: "Add a step to a task",
				Flags: []cli.Flag{listFlag, taskFlag, nameFlag, checkedFlag, dataFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					b, err := body(s.c)
					if err != nil {
						return err
					}
					if b["displayName"] == nil {
						return fmt.Errorf("a step needs a name: set --name")
					}
					it, err := s.cl.CreateChecklistItem(s.c.Context, t, b)
					if err != nil {
						return err
					}
					return s.done(it, "Added the step %s (%s)", it.DisplayName, it.ID)
				}),
			}),
			leaf(&cli.Command{
				Name: "update", Usage: "Change a step",
				Flags: []cli.Flag{listFlag, taskFlag, idFlag, nameFlag, checkedFlag, dataFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					b, err := body(s.c)
					if err != nil {
						return err
					}
					if len(b) == 0 {
						return fmt.Errorf("nothing to change: set --name, --checked, or --data")
					}
					if _, ok := b["isChecked"]; !ok { // Graph resets a missing isChecked to false
						cur, err := s.cl.ChecklistItem(s.c.Context, t, s.c.String("id"))
						if err != nil {
							return err
						}
						b["isChecked"] = cur.IsChecked
					}
					it, err := s.cl.UpdateChecklistItem(s.c.Context, t, s.c.String("id"), b)
					if err != nil {
						return err
					}
					return s.done(it, "Updated the step %s", it.DisplayName)
				}),
			}),
			leaf(&cli.Command{Name: "check", Usage: "Mark a step as done", Flags: []cli.Flag{listFlag, taskFlag, idFlag}, Action: check(true, "Checked")}),
			leaf(&cli.Command{Name: "uncheck", Usage: "Mark a step as not done", Flags: []cli.Flag{listFlag, taskFlag, idFlag}, Action: check(false, "Unchecked")}),
			leaf(&cli.Command{
				Name: "delete", Usage: "Delete a step",
				Flags: []cli.Flag{listFlag, taskFlag, idFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					id := s.c.String("id")
					if err := s.cl.DeleteChecklistItem(s.c.Context, t, id); err != nil {
						return err
					}
					return s.done(map[string]string{"deleted": id}, "Deleted the step %s", id)
				}),
			}),
		},
	}
}

func (s *session) renderSteps(items []client.ChecklistItem) error {
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		done := ""
		if it.IsChecked {
			done = "x"
		}
		rows = append(rows, []string{it.ID, done, it.DisplayName})
	}
	return s.render(items, []string{"ID", "DONE", "NAME"}, rows)
}

// LinksCommand returns the `links` command: the linked resources of a task.
func LinksCommand() *cli.Command {
	fields := []cli.Flag{
		&cli.StringFlag{Name: "url", Usage: "web URL of the item"},
		&cli.StringFlag{Name: "app", Usage: "name of the app of the item"},
		&cli.StringFlag{Name: "name", Usage: "title of the item"},
		&cli.StringFlag{Name: "external-id", Usage: "id of the item in its app"},
	}
	body := func(c *cli.Context) map[string]any {
		b := map[string]any{}
		for flag, field := range map[string]string{"url": "webUrl", "app": "applicationName", "name": "displayName", "external-id": "externalId"} {
			if c.IsSet(flag) {
				b[field] = c.String(flag)
			}
		}
		return b
	}
	return &cli.Command{
		Name:  "links",
		Usage: "Work with the linked resources of a task",
		Subcommands: []*cli.Command{
			leaf(&cli.Command{
				Name: "list", Usage: "Show the links of a task",
				Flags: []cli.Flag{listFlag, taskFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					l, err := s.cl.LinkedResources(s.c.Context, t)
					if err != nil {
						return err
					}
					return s.renderLinks(l)
				}),
			}),
			leaf(&cli.Command{
				Name: "get", Usage: "Show one link",
				Flags: []cli.Flag{listFlag, taskFlag, idFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					l, err := s.cl.LinkedResource(s.c.Context, t, s.c.String("id"))
					if err != nil {
						return err
					}
					return s.one(l, func() error { return s.renderLinks([]client.LinkedResource{l}) })
				}),
			}),
			leaf(&cli.Command{
				Name: "create", Usage: "Add a link to a task",
				Flags: append([]cli.Flag{listFlag, taskFlag}, fields...),
				Action: taskRun(func(s *session, t client.Ref) error {
					b := body(s.c)
					if b["applicationName"] == nil || b["displayName"] == nil {
						return fmt.Errorf("a link needs --app and --name")
					}
					l, err := s.cl.CreateLinkedResource(s.c.Context, t, b)
					if err != nil {
						return err
					}
					return s.done(l, "Added the link %s (%s)", l.DisplayName, l.ID)
				}),
			}),
			leaf(&cli.Command{
				Name: "update", Usage: "Change a link",
				Flags: append([]cli.Flag{listFlag, taskFlag, idFlag}, fields...),
				Action: taskRun(func(s *session, t client.Ref) error {
					b := body(s.c)
					if len(b) == 0 {
						return fmt.Errorf("nothing to change: set a field flag, see --help")
					}
					l, err := s.cl.UpdateLinkedResource(s.c.Context, t, s.c.String("id"), b)
					if err != nil {
						return err
					}
					return s.done(l, "Updated the link %s", l.DisplayName)
				}),
			}),
			leaf(&cli.Command{
				Name: "delete", Usage: "Delete a link",
				Flags: []cli.Flag{listFlag, taskFlag, idFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					id := s.c.String("id")
					if err := s.cl.DeleteLinkedResource(s.c.Context, t, id); err != nil {
						return err
					}
					return s.done(map[string]string{"deleted": id}, "Deleted the link %s", id)
				}),
			}),
		},
	}
}

func (s *session) renderLinks(links []client.LinkedResource) error {
	rows := make([][]string, 0, len(links))
	for _, l := range links {
		rows = append(rows, []string{l.ID, l.ApplicationName, l.DisplayName, l.WebURL})
	}
	return s.render(links, []string{"ID", "APP", "NAME", "URL"}, rows)
}

// AttachmentsCommand returns the `attachments` command: the files of a task.
func AttachmentsCommand() *cli.Command {
	return &cli.Command{
		Name:  "attachments",
		Usage: "Work with the files of a task",
		Subcommands: []*cli.Command{
			leaf(&cli.Command{
				Name: "list", Usage: "Show the files of a task",
				Flags: []cli.Flag{listFlag, taskFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					l, err := s.cl.Attachments(s.c.Context, t)
					if err != nil {
						return err
					}
					rows := make([][]string, 0, len(l))
					for _, a := range l {
						rows = append(rows, []string{a.ID, a.Name, a.ContentType, fmt.Sprint(a.Size)})
					}
					return s.render(l, []string{"ID", "NAME", "TYPE", "SIZE"}, rows)
				}),
			}),
			leaf(&cli.Command{
				Name: "get", Usage: "Download a file",
				Flags: []cli.Flag{listFlag, taskFlag, idFlag,
					&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "file to write (default: the attachment name in the current folder; - is stdout)"},
				},
				Action: taskRun(func(s *session, t client.Ref) error {
					id := s.c.String("id")
					meta, err := s.cl.Attachment(s.c.Context, t, id)
					if err != nil {
						return err
					}
					data := meta.ContentBytes
					if data == nil {
						if data, err = s.cl.AttachmentContent(s.c.Context, t, id); err != nil {
							return err
						}
					}
					out := s.c.String("output")
					if out == "-" {
						_, err := s.c.App.Writer.Write(data)
						return err
					}
					if out == "" {
						out = filepath.Base(meta.Name)
					}
					if err := os.WriteFile(out, data, 0o600); err != nil {
						return err
					}
					meta.ContentBytes = nil
					return s.done(map[string]any{"attachment": meta, "path": out}, "Wrote %d bytes to %s", len(data), out)
				}),
			}),
			leaf(&cli.Command{
				Name: "add", Usage: "Add a file to a task (at most 25 MB)",
				Flags: []cli.Flag{listFlag, taskFlag,
					&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "file to add", Required: true},
					&cli.StringFlag{Name: "name", Usage: "attachment name (default: the file name)"},
					&cli.StringFlag{Name: "content-type", Usage: "MIME type (default: from the file)"},
				},
				Action: taskRun(func(s *session, t client.Ref) error {
					path := s.c.String("file")
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					name := s.c.String("name")
					if name == "" {
						name = filepath.Base(path)
					}
					ct := s.c.String("content-type")
					if ct == "" {
						ct = mime.TypeByExtension(filepath.Ext(path))
					}
					a, err := s.cl.AddAttachment(s.c.Context, t, name, ct, data)
					if err != nil {
						return err
					}
					a.ContentBytes = nil
					return s.done(a, "Added the file %s (%s)", a.Name, a.ID)
				}),
			}),
			leaf(&cli.Command{
				Name: "delete", Usage: "Delete a file",
				Flags: []cli.Flag{listFlag, taskFlag, idFlag},
				Action: taskRun(func(s *session, t client.Ref) error {
					id := s.c.String("id")
					if err := s.cl.DeleteAttachment(s.c.Context, t, id); err != nil {
						return err
					}
					return s.done(map[string]string{"deleted": id}, "Deleted the file %s", id)
				}),
			}),
		},
	}
}
