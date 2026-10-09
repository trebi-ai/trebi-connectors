package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/trebi-ai/trebi-connectors/connectors/microsoft-todo-cli/internal/client"
)

// ExtensionsCommand returns the `extensions` command: the open extensions
// of a list, or of a task with --task.
func ExtensionsCommand() *cli.Command {
	ownerTask := &cli.StringFlag{Name: "task", Aliases: []string{"t"}, Usage: "task id (without it, the extension is on the list)"}
	nameFlag := &cli.StringFlag{Name: "name", Usage: "extension name, for example com.example.app", Required: true}
	dataFlag := &cli.StringFlag{Name: "data", Usage: "extension fields as JSON, or @file", Required: true}
	run := func(fn func(s *session, owner client.Ref) error) cli.ActionFunc {
		return func(c *cli.Context) error {
			s, err := newSession(c)
			if err != nil {
				return err
			}
			l, err := s.list()
			if err != nil {
				return err
			}
			return fn(s, client.Ref{List: l.ID, Task: c.String("task")})
		}
	}
	return &cli.Command{
		Name:  "extensions",
		Usage: "Work with the open extensions (custom data) of a list or a task",
		Subcommands: []*cli.Command{
			leaf(&cli.Command{
				Name: "list", Usage: "Show the extensions",
				Flags: []cli.Flag{listFlag, ownerTask},
				Action: run(func(s *session, o client.Ref) error {
					l, err := s.cl.Extensions(s.c.Context, o)
					if client.IsNotFound(err) {
						return fmt.Errorf("%w (Microsoft does not list extensions for some accounts; use extensions get --name)", err)
					}
					if err != nil {
						return err
					}
					return printJSON(s.c.App.Writer, l)
				}),
			}),
			leaf(&cli.Command{
				Name: "get", Usage: "Show one extension",
				Flags: []cli.Flag{listFlag, ownerTask, nameFlag},
				Action: run(func(s *session, o client.Ref) error {
					x, err := s.cl.Extension(s.c.Context, o, s.c.String("name"))
					if err != nil {
						return err
					}
					return printJSON(s.c.App.Writer, x)
				}),
			}),
			leaf(&cli.Command{
				Name: "create", Usage: "Add an extension",
				Flags: []cli.Flag{listFlag, ownerTask, nameFlag, dataFlag},
				Action: run(func(s *session, o client.Ref) error {
					data, err := readData(s.c.String("data"))
					if err != nil {
						return err
					}
					x, err := s.cl.CreateExtension(s.c.Context, o, s.c.String("name"), data)
					if err != nil {
						return err
					}
					return printJSON(s.c.App.Writer, x)
				}),
			}),
			leaf(&cli.Command{
				Name: "update", Usage: "Change the fields of an extension",
				Flags: []cli.Flag{listFlag, ownerTask, nameFlag, dataFlag},
				Action: run(func(s *session, o client.Ref) error {
					data, err := readData(s.c.String("data"))
					if err != nil {
						return err
					}
					name := s.c.String("name")
					if err := s.cl.UpdateExtension(s.c.Context, o, name, data); err != nil {
						return err
					}
					return s.done(map[string]string{"updated": name}, "Updated the extension %s", name)
				}),
			}),
			leaf(&cli.Command{
				Name: "delete", Usage: "Delete an extension",
				Flags: []cli.Flag{listFlag, ownerTask, nameFlag},
				Action: run(func(s *session, o client.Ref) error {
					name := s.c.String("name")
					if err := s.cl.DeleteExtension(s.c.Context, o, name); err != nil {
						return err
					}
					return s.done(map[string]string{"deleted": name}, "Deleted the extension %s", name)
				}),
			}),
		},
	}
}

// SubscriptionsCommand returns the `subscriptions` command: Graph change
// notifications for the tasks of a list.
func SubscriptionsCommand() *cli.Command {
	expires := &cli.DurationFlag{Name: "expires", Value: 4230 * time.Minute, Usage: "time until the subscription ends (at most 70h30m)"}
	render := func(s *session, subs []client.Subscription) error {
		rows := make([][]string, 0, len(subs))
		for _, x := range subs {
			rows = append(rows, []string{x.ID, x.ChangeType, x.ExpirationDateTime.Format(time.RFC3339), x.Resource, x.NotificationURL})
		}
		return s.render(subs, []string{"ID", "CHANGES", "EXPIRES", "RESOURCE", "URL"}, rows)
	}
	run := func(fn func(s *session) error) cli.ActionFunc {
		return func(c *cli.Context) error {
			s, err := newSession(c)
			if err != nil {
				return err
			}
			return fn(s)
		}
	}
	return &cli.Command{
		Name:  "subscriptions",
		Usage: "Manage Graph change notifications (webhooks) for the tasks of a list",
		Subcommands: []*cli.Command{
			leaf(&cli.Command{
				Name: "list", Usage: "Show the subscriptions of this app",
				Action: run(func(s *session) error {
					subs, err := s.cl.Subscriptions(s.c.Context)
					if err != nil {
						return err
					}
					return render(s, subs)
				}),
			}),
			leaf(&cli.Command{
				Name: "get", Usage: "Show one subscription",
				Flags: []cli.Flag{idFlag},
				Action: run(func(s *session) error {
					x, err := s.cl.Subscription(s.c.Context, s.c.String("id"))
					if err != nil {
						return err
					}
					return s.one(x, func() error { return render(s, []client.Subscription{x}) })
				}),
			}),
			leaf(&cli.Command{
				Name:  "create",
				Usage: "Subscribe a public HTTPS URL to the task changes of a list",
				Description: "Graph sends a validationToken query to the URL first. The URL must send back the token as text/plain " +
					"with status 200 in 10 seconds.",
				Flags: []cli.Flag{listFlag,
					&cli.StringFlag{Name: "url", Usage: "notification URL (HTTPS)", Required: true},
					&cli.StringFlag{Name: "lifecycle-url", Usage: "URL for lifecycle notifications"},
					&cli.StringFlag{Name: "change-type", Value: "created,updated,deleted", Usage: "changes to send"},
					&cli.StringFlag{Name: "client-state", Usage: "secret that Graph sends back in each notification (default: a random value)"},
					expires,
				},
				Action: run(func(s *session) error {
					l, err := s.list()
					if err != nil {
						return err
					}
					state := s.c.String("client-state")
					if state == "" {
						state = client.Secret(16)
					}
					x, err := s.cl.CreateSubscription(s.c.Context, client.Subscription{
						Resource: client.TaskResource(l.ID), ChangeType: s.c.String("change-type"),
						NotificationURL: s.c.String("url"), LifecycleNotificationURL: s.c.String("lifecycle-url"),
						ClientState: state, ExpirationDateTime: time.Now().Add(s.c.Duration("expires")).UTC(),
					})
					if err != nil {
						return err
					}
					x.ClientState = state
					return s.done(x, "Created the subscription %s (client state %s, expires %s)", x.ID, state, x.ExpirationDateTime.Format(time.RFC3339))
				}),
			}),
			leaf(&cli.Command{
				Name: "renew", Usage: "Move the expiry of a subscription",
				Flags: []cli.Flag{idFlag, expires},
				Action: run(func(s *session) error {
					x, err := s.cl.RenewSubscription(s.c.Context, s.c.String("id"), time.Now().Add(s.c.Duration("expires")))
					if err != nil {
						return err
					}
					return s.done(x, "Renewed the subscription %s to %s", x.ID, x.ExpirationDateTime.Format(time.RFC3339))
				}),
			}),
			leaf(&cli.Command{
				Name: "delete", Usage: "Delete a subscription",
				Flags: []cli.Flag{idFlag},
				Action: run(func(s *session) error {
					id := s.c.String("id")
					if err := s.cl.DeleteSubscription(s.c.Context, id); err != nil {
						return err
					}
					return s.done(map[string]string{"deleted": id}, "Deleted the subscription %s", id)
				}),
			}),
		},
	}
}

// change is one line of `watch`.
type change struct {
	Change string      `json:"change"` // created, updated, or deleted
	List   string      `json:"list"`
	ListID string      `json:"list_id"`
	Task   client.Task `json:"task"`
	At     time.Time   `json:"at"`
}

// WatchCommand returns the `watch` command: it polls delta queries and
// writes one JSON line for each task change. It needs no public URL.
func WatchCommand() *cli.Command {
	return &cli.Command{
		Name:  "watch",
		Usage: "Write one JSON line for each task change of the lists, until stopped",
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "list", Aliases: []string{"l"}, Usage: "list id or name (repeat the flag for more)", Required: true},
			&cli.DurationFlag{Name: "interval", Value: 30 * time.Second, Usage: "time between two polls"},
			&cli.BoolFlag{Name: "from-start", Usage: "also write the tasks that exist now, as created"},
		},
		Action: func(c *cli.Context) error {
			s, err := newSession(c)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
			defer stop()
			var lists []client.List
			for _, name := range c.StringSlice("list") {
				l, err := s.listNamed(name)
				if err != nil {
					return err
				}
				lists = append(lists, l)
			}
			enc := json.NewEncoder(c.App.Writer)
			links, gone := map[string]string{}, map[string]bool{}
			for _, l := range lists {
				tasks, link, err := s.cl.Delta(ctx, l.ID, "")
				if err != nil {
					return err
				}
				links[l.ID] = link
				if c.Bool("from-start") {
					for _, t := range tasks {
						if err := enc.Encode(change{"created", l.DisplayName, l.ID, t, time.Now().UTC()}); err != nil {
							return err
						}
					}
				}
			}
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(c.Duration("interval")):
				}
				for _, l := range lists {
					tasks, link, err := s.cl.Delta(ctx, l.ID, links[l.ID])
					if ctx.Err() != nil {
						return nil
					}
					if err != nil {
						fmt.Fprintf(c.App.ErrWriter, "watch %s: %v\n", l.DisplayName, err) //nolint:errcheck // a log line
						continue
					}
					links[l.ID] = link
					for _, t := range tasks {
						kind := "updated"
						switch {
						case t.Removed != nil && gone[t.ID]:
							continue // Graph repeats a removal in the next round
						case t.Removed != nil:
							kind, gone[t.ID] = "deleted", true
						case t.IsNew():
							kind = "created"
						}
						if err := enc.Encode(change{kind, l.DisplayName, l.ID, t, time.Now().UTC()}); err != nil {
							return err
						}
					}
				}
			}
		},
	}
}
