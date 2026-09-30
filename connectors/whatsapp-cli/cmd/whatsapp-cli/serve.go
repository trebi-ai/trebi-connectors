package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/app"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/ipc"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/store"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa/fakewa"
	"github.com/trebi-ai/trebi-connectors/sdk"
)

func newServeCmd(flags *rootFlags) *cobra.Command {
	var sandbox bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the trebi-connector/1 protocol on stdin and stdout",
		Long:  "Serve runs the channel adapter for the Trebi daemon. It holds the WhatsApp connection, so the send commands of this CLI forward to it. With --sandbox, it runs the same adapter over a fake WhatsApp and needs no account. Outside Trebi, the sandbox uses a temp store unless --store is set.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signalContext()
			defer stop()
			opts := app.Options{StoreDir: resolveStoreDir(flags), Version: version, JSON: flags.asJSON, AllowUnauthed: true}
			if sandbox {
				if !cmd.Flag("store").Changed && !config.Trebi() {
					dir, err := os.MkdirTemp("", "whatsapp-cli-sandbox-")
					if err != nil {
						return err
					}
					defer os.RemoveAll(dir) //nolint:errcheck // a temp folder
					opts.StoreDir = dir
				}
				fake, err := fakewa.Sandbox(opts.StoreDir)
				if err != nil {
					return err
				}
				opts.WA = fake
			}
			a, lk, err := openApp(opts, true)
			if nerr := (*store.NewerSchemaError)(nil); errors.As(err, &nerr) {
				return sdk.Serve(ctx, storeError{err: nerr})
			}
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
	cmd.Flags().BoolVar(&sandbox, "sandbox", false, "run the adapter over a fake WhatsApp with no account")
	return cmd
}

// storeError is the adapter of a store that this version cannot open. It
// reports status error and keeps the store as it is.
type storeError struct{ err error }

func (s storeError) Initialize(context.Context, sdk.InitializeParams) (sdk.InitializeResult, error) {
	return sdk.InitializeResult{Adapter: sdk.AdapterInfo{Name: app.AdapterName, Version: version}}, nil
}

func (s storeError) AuthStatus(context.Context) (sdk.AuthState, error) {
	return sdk.AuthState{}, s.err
}
