package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	appPkg "github.com/flarco/cli-tools/whatsapp-cli/internal/app"
	"github.com/spf13/cobra"
)

// allCategories lists every category except "history", which is opt-in due to payload size.
var allCategories = []string{
	"messages", "receipts", "connection", "calls", "presence",
	"groups", "appstate", "newsletters", "media", "security",
}

// knownCategories is allCategories + history + the special "all" keyword.
var knownCategories = map[string]bool{
	"messages":    true,
	"receipts":    true,
	"connection":  true,
	"calls":       true,
	"presence":    true,
	"groups":      true,
	"appstate":    true,
	"newsletters": true,
	"media":       true,
	"security":    true,
	"history":     true,
	"all":         true,
}

const defaultEvents = "messages,receipts,connection"

func newListenCmd(flags *rootFlags) *cobra.Command {
	var eventsFlag string
	var chatFilter string
	var fromFilter string
	var excludeSelf bool
	var raw bool
	var maxReconnect time.Duration
	var presence string

	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Stream live WhatsApp events as JSONL on stdout",
		Long: `Stream live WhatsApp events as JSONL on stdout.

Each line is a JSON object of the form {"t":"<event>","ts":"<rfc3339>","d":{...}}.
Status messages are written to stderr.

Requires a prior 'whatsapp-cli auth'. Acquires an exclusive lock on the store directory,
so it cannot run concurrently with 'whatsapp-cli sync' on the same store.

While listening, 'whatsapp-cli send' commands are forwarded to this process over a Unix
socket in the store directory and sent on the live connection, so you can send
messages without stopping the listener.

A second 'whatsapp-cli listen' on the same store attaches to the first (the anchor)
instead of failing on the lock: it registers its own --events/--chat/--from
filter and streams matching events to its own stdout. When the anchor exits, the
attached listeners print a notice and exit non-zero.

Use --presence available so the session is online and delivery receipts (two gray
checks) are sent when messages arrive. Read receipts still require 'send receipt'.

Event categories: ` + strings.Join(allCategories, ", ") + `, history, all
(history is opt-in: it delivers large initial-sync batches).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			categories, err := parseEventCategories(eventsFlag)
			if err != nil {
				return err
			}
			if strings.TrimSpace(presence) != "" {
				if _, _, err := parsePresenceState(presence); err != nil {
					return fmt.Errorf("--presence: %w", err)
				}
			}

			ctx, stop := signalContext()
			defer stop()

			filter := appPkg.ListenFilter{
				Categories:  categories,
				ChatFilter:  strings.TrimSpace(chatFilter),
				FromFilter:  strings.TrimSpace(fromFilter),
				ExcludeSelf: excludeSelf,
				Raw:         raw,
			}

			return runListen(ctx, flags, filter, maxReconnect, strings.TrimSpace(presence))
		},
	}

	cmd.Flags().StringVarP(&eventsFlag, "events", "e", defaultEvents, "comma-separated event categories (or 'all')")
	cmd.Flags().StringVar(&chatFilter, "chat", "", "filter to a single chat JID")
	cmd.Flags().StringVar(&fromFilter, "from", "", "filter by sender JID")
	cmd.Flags().BoolVar(&excludeSelf, "exclude-self", false, "drop events where from_me=true")
	cmd.Flags().BoolVar(&raw, "raw", false, "emit raw whatsmeow event instead of normalized shape")
	cmd.Flags().DurationVar(&maxReconnect, "max-reconnect", 5*time.Minute, "give up reconnecting after this long from first disconnect (backoff 2s..30s; 0 = unlimited)")
	cmd.Flags().StringVar(&presence, "presence", "", "send global presence after connect: available or unavailable")
	return cmd
}

func parseEventCategories(raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultEvents
	}

	requested := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if !knownCategories[name] {
			sorted := append([]string{}, allCategories...)
			sorted = append(sorted, "history", "all")
			sort.Strings(sorted)
			return nil, fmt.Errorf("unknown event category %q (known: %s)", name, strings.Join(sorted, ", "))
		}
		requested[name] = true
	}

	if requested["all"] {
		for _, c := range allCategories {
			requested[c] = true
		}
		delete(requested, "all")
		// history stays opt-in even under "all"
	}

	if len(requested) == 0 {
		return nil, fmt.Errorf("no event categories selected")
	}
	return requested, nil
}
