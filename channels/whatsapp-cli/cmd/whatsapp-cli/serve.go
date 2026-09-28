package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/trebi-ai/trebi-connectors/channels/whatsapp-cli/internal/app"
	"github.com/trebi-ai/trebi-connectors/channels/whatsapp-cli/internal/ipc"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

func newServeCmd(flags *rootFlags) *cobra.Command {
	var sandbox bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the trebi-connector/1 protocol on stdin and stdout",
		Long:  "Serve runs the channel adapter for the Trebi daemon. It holds the WhatsApp connection, so the send commands of this CLI forward to it. With --sandbox, it serves fake rooms and needs no account.",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, stop := signalContext()
			defer stop()
			if sandbox {
				return sdk.Serve(ctx, sdk.NewSandbox(sdk.SandboxConfig{
					Adapter: sdk.AdapterInfo{Name: app.AdapterName, Version: version},
					Events:  app.AdapterEvents, Features: app.AdapterFeatures, Limits: app.AdapterLimits, Login: app.AdapterLogin,
				}))
			}
			a, lk, err := newApp(ctx, flags, true, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)
			if err := a.OpenWA(); err != nil {
				return err
			}
			if srv, err := ipc.Serve(ctx, a.StoreDir(), sendHandler(a), nil); err != nil {
				fmt.Fprintf(os.Stderr, "send forwarding is off: %v\n", err)
			} else {
				defer srv.Close()
			}
			return sdk.Serve(ctx, app.NewAdapter(a))
		},
	}
	cmd.Flags().BoolVar(&sandbox, "sandbox", false, "serve an in-memory sandbox with no account")
	return cmd
}
