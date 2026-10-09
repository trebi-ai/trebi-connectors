package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/mstodo-cli/internal/client"
)

// ListsCommand returns the `lists` command.
func ListsCommand() *cli.Command {
	return &cli.Command{
		Name:  "lists",
		Usage: "Work with To Do lists",
		Subcommands: []*cli.Command{
			leaf(&cli.Command{
				Name:  "list",
				Usage: "Show the To Do lists",
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					lists, err := s.cl.Lists(c.Context)
					if err != nil {
						return err
					}
					return s.renderLists(lists)
				},
			}),
			leaf(&cli.Command{
				Name:  "get",
				Usage: "Show one list",
				Flags: []cli.Flag{listFlag},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					l, err := s.list()
					if err != nil {
						return err
					}
					return s.one(l, func() error { return s.renderLists([]client.List{l}) })
				},
			}),
			leaf(&cli.Command{
				Name:  "create",
				Usage: "Create a list",
				Flags: []cli.Flag{&cli.StringFlag{Name: "name", Usage: "list name", Required: true}},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					l, err := s.cl.CreateList(c.Context, c.String("name"))
					if err != nil {
						return err
					}
					return s.done(l, "Created the list %s (%s)", l.DisplayName, l.ID)
				},
			}),
			leaf(&cli.Command{
				Name:  "update",
				Usage: "Rename a list",
				Flags: []cli.Flag{listFlag, &cli.StringFlag{Name: "name", Usage: "new list name", Required: true}},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					l, err := s.list()
					if err != nil {
						return err
					}
					l, err = s.cl.UpdateList(c.Context, l.ID, map[string]string{"displayName": c.String("name")})
					if err != nil {
						return err
					}
					return s.done(l, "Renamed the list to %s", l.DisplayName)
				},
			}),
			leaf(&cli.Command{
				Name:  "delete",
				Usage: "Delete a list and its tasks",
				Flags: []cli.Flag{listFlag},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					l, err := s.list()
					if err != nil {
						return err
					}
					if err := s.cl.DeleteList(c.Context, l.ID); err != nil {
						return err
					}
					return s.done(map[string]string{"deleted": l.ID}, "Deleted the list %s", l.DisplayName)
				},
			}),
			leaf(&cli.Command{
				Name:  "delta",
				Usage: "Show the list changes since a delta link (all lists without --link)",
				Flags: []cli.Flag{&cli.StringFlag{Name: "link", Usage: "delta link from an earlier run"}},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					lists, link, err := s.cl.ListsDelta(c.Context, c.String("link"))
					if err != nil {
						return err
					}
					return printJSON(c.App.Writer, map[string]any{"value": lists, "delta_link": link})
				},
			}),
		},
	}
}

func (s *session) renderLists(lists []client.List) error {
	rows := make([][]string, 0, len(lists))
	for _, l := range lists {
		rows = append(rows, []string{l.ID, l.DisplayName, l.WellknownListName})
	}
	return s.render(lists, []string{"ID", "NAME", "KIND"}, rows)
}

// taskFlags set the fields of a task.
var taskFlags = []cli.Flag{
	&cli.StringFlag{Name: "title", Usage: "task title"},
	&cli.StringFlag{Name: "note", Usage: "task note as text"},
	&cli.StringFlag{Name: "note-html", Usage: "task note as HTML"},
	&cli.StringFlag{Name: "due", Usage: "due date: YYYY-MM-DD, or none to remove it"},
	&cli.StringFlag{Name: "start", Usage: "start date: YYYY-MM-DD[THH:MM], or none to remove it"},
	&cli.StringFlag{Name: "reminder", Usage: "reminder time: YYYY-MM-DDTHH:MM"},
	&cli.BoolFlag{Name: "no-reminder", Usage: "turn off the reminder"},
	&cli.StringFlag{Name: "tz", Value: "UTC", Usage: "time zone of --due, --start, and --reminder"},
	&cli.StringFlag{Name: "importance", Usage: "low, normal, or high"},
	&cli.StringFlag{Name: "status", Usage: "notStarted, inProgress, completed, waitingOnOthers, or deferred"},
	&cli.StringSliceFlag{Name: "category", Usage: "category (repeat the flag for more)"},
	&cli.StringFlag{Name: "recurrence", Usage: "patternedRecurrence as JSON, or none to remove it"},
	&cli.StringFlag{Name: "data", Usage: "more todoTask fields as JSON, or @file"},
}

// taskBody builds a todoTask from the field flags. --data comes first, so
// a named flag wins.
func taskBody(c *cli.Context) (map[string]any, error) {
	body, err := readData(c.String("data"))
	if err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	tz := c.String("tz")
	for _, f := range []struct{ flag, field string }{{"title", "title"}, {"importance", "importance"}, {"status", "status"}} {
		if c.IsSet(f.flag) {
			body[f.field] = c.String(f.flag)
		}
	}
	switch {
	case c.IsSet("note-html"):
		body["body"] = client.ItemBody{Content: c.String("note-html"), ContentType: "html"}
	case c.IsSet("note"):
		body["body"] = client.ItemBody{Content: c.String("note"), ContentType: "text"}
	}
	for _, f := range []struct{ flag, field string }{{"due", "dueDateTime"}, {"start", "startDateTime"}} {
		if c.IsSet(f.flag) {
			if body[f.field], err = dateTime(c.String(f.flag), tz); err != nil {
				return nil, fmt.Errorf("--%s: %w", f.flag, err)
			}
		}
	}
	if c.IsSet("reminder") {
		if body["reminderDateTime"], err = dateTime(c.String("reminder"), tz); err != nil {
			return nil, fmt.Errorf("--reminder: %w", err)
		}
		body["isReminderOn"] = true
	}
	if c.Bool("no-reminder") {
		body["isReminderOn"] = false
	}
	if c.IsSet("category") {
		body["categories"] = c.StringSlice("category")
	}
	if c.IsSet("recurrence") {
		if v := c.String("recurrence"); v == "none" {
			body["recurrence"] = nil
		} else {
			var r map[string]any
			if err := json.Unmarshal([]byte(v), &r); err != nil {
				return nil, fmt.Errorf("--recurrence is not a JSON object: %w", err)
			}
			body["recurrence"] = r
		}
	}
	return body, nil
}

// dateTime turns a date or a date and time into a dateTimeTimeZone. The
// value none gives null, which removes the field.
func dateTime(v, tz string) (*client.DateTimeZone, error) {
	if v == "none" {
		return nil, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return &client.DateTimeZone{DateTime: t.Format("2006-01-02T15:04:05"), TimeZone: tz}, nil
		}
	}
	return nil, fmt.Errorf("%q is not YYYY-MM-DD or YYYY-MM-DDTHH:MM", v)
}

// TasksCommand returns the `tasks` command.
func TasksCommand() *cli.Command {
	return &cli.Command{
		Name:  "tasks",
		Usage: "Work with the tasks of a list",
		Subcommands: []*cli.Command{
			leaf(&cli.Command{
				Name:  "list",
				Usage: "Show the tasks of a list",
				Flags: []cli.Flag{
					listFlag,
					&cli.StringFlag{Name: "status", Usage: "open (not completed), or a task status"},
					&cli.StringFlag{Name: "filter", Usage: "OData $filter"},
					&cli.StringFlag{Name: "orderby", Usage: "OData $orderby, for example dueDateTime/dateTime"},
					&cli.StringFlag{Name: "select", Usage: "OData $select"},
					&cli.IntFlag{Name: "limit", Usage: "most tasks to show (0 shows all)"},
				},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					l, err := s.list()
					if err != nil {
						return err
					}
					q := client.Query{Filter: c.String("filter"), OrderBy: c.String("orderby"), Select: c.String("select"), Limit: c.Int("limit")}
					if st := c.String("status"); st != "" {
						cond := fmt.Sprintf("status eq '%s'", st)
						if st == "open" {
							cond = "status ne 'completed'"
						}
						if q.Filter != "" {
							cond += " and (" + q.Filter + ")"
						}
						q.Filter = cond
					}
					tasks, err := s.cl.Tasks(c.Context, l.ID, q)
					if err != nil {
						return err
					}
					return s.renderTasks(tasks)
				},
			}),
			leaf(&cli.Command{
				Name:  "get",
				Usage: "Show one task",
				Flags: []cli.Flag{listFlag, taskFlag},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					r, err := s.task()
					if err != nil {
						return err
					}
					t, err := s.cl.Task(c.Context, r.List, r.Task)
					if err != nil {
						return err
					}
					return s.one(t, func() error { return s.renderTasks([]client.Task{t}) })
				},
			}),
			leaf(&cli.Command{
				Name:  "create",
				Usage: "Create a task",
				Flags: append([]cli.Flag{listFlag}, taskFlags...),
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					body, err := taskBody(c)
					if err != nil {
						return err
					}
					if body["title"] == nil || body["title"] == "" {
						return fmt.Errorf("a task needs a title: set --title")
					}
					l, err := s.list()
					if err != nil {
						return err
					}
					t, err := s.cl.CreateTask(c.Context, l.ID, body)
					if err != nil {
						return err
					}
					return s.done(t, "Created the task %s (%s)", t.Title, t.ID)
				},
			}),
			leaf(&cli.Command{
				Name:  "update",
				Usage: "Change a task",
				Flags: append([]cli.Flag{listFlag, taskFlag}, taskFlags...),
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					body, err := taskBody(c)
					if err != nil {
						return err
					}
					if len(body) == 0 {
						return fmt.Errorf("nothing to change: set a field flag, see --help")
					}
					return s.patchTask(body, "Updated")
				},
			}),
			leaf(&cli.Command{
				Name:  "complete",
				Usage: "Mark a task as completed",
				Flags: []cli.Flag{listFlag, taskFlag},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					return s.patchTask(map[string]any{"status": "completed"}, "Completed")
				},
			}),
			leaf(&cli.Command{
				Name:  "reopen",
				Usage: "Mark a task as not started",
				Flags: []cli.Flag{listFlag, taskFlag},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					return s.patchTask(map[string]any{"status": "notStarted"}, "Reopened")
				},
			}),
			leaf(&cli.Command{
				Name:  "delete",
				Usage: "Delete a task",
				Flags: []cli.Flag{listFlag, taskFlag},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					r, err := s.task()
					if err != nil {
						return err
					}
					if err := s.cl.DeleteTask(c.Context, r.List, r.Task); err != nil {
						return err
					}
					return s.done(map[string]string{"deleted": r.Task}, "Deleted the task %s", r.Task)
				},
			}),
			leaf(&cli.Command{
				Name:  "delta",
				Usage: "Show the task changes since a delta link (all tasks without --link)",
				Flags: []cli.Flag{listFlag, &cli.StringFlag{Name: "link", Usage: "delta link from an earlier run"}},
				Action: func(c *cli.Context) error {
					s, err := newSession(c)
					if err != nil {
						return err
					}
					l, err := s.list()
					if err != nil {
						return err
					}
					tasks, link, err := s.cl.Delta(c.Context, l.ID, c.String("link"))
					if err != nil {
						return err
					}
					return printJSON(c.App.Writer, map[string]any{"value": tasks, "delta_link": link})
				},
			}),
		},
	}
}

func (s *session) patchTask(body map[string]any, verb string) error {
	r, err := s.task()
	if err != nil {
		return err
	}
	t, err := s.cl.UpdateTask(s.c.Context, r.List, r.Task, body)
	if err != nil {
		return err
	}
	return s.done(t, "%s the task %s", verb, t.Title)
}

func (s *session) renderTasks(tasks []client.Task) error {
	rows := make([][]string, 0, len(tasks))
	for _, t := range tasks {
		due := ""
		if t.DueDateTime != nil {
			due, _, _ = strings.Cut(t.DueDateTime.DateTime, "T")
		}
		rows = append(rows, []string{t.ID, t.Status, t.Importance, due, t.Title})
	}
	return s.render(tasks, []string{"ID", "STATUS", "IMPORTANCE", "DUE", "TITLE"}, rows)
}
