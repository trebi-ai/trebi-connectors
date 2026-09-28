package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/app"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/config"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/lock"
	"github.com/trebi-ai/trebi-connectors/src/whatsapp-cli/internal/out"
)

var version = "dev"

// init takes the module version when the build did not set one, as with
// go install.
func init() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

type rootFlags struct {
	storeDir string
	asJSON   bool
	timeout  time.Duration
}

func execute(args []string) error {
	var flags rootFlags

	rootCmd := &cobra.Command{
		Use:           "whatsapp-cli",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	rootCmd.SetVersionTemplate("whatsapp-cli {{.Version}}\n")

	rootCmd.PersistentFlags().StringVar(&flags.storeDir, "store", "", "store directory (default: $TREBI_STATE_DIR, $WHATSAPP_CLI_STORE_DIR, or ~/.whatsapp-cli)")
	rootCmd.PersistentFlags().BoolVar(&flags.asJSON, "json", false, "output JSON instead of human-readable text")
	rootCmd.PersistentFlags().DurationVar(&flags.timeout, "timeout", 5*time.Minute, "command timeout (non-sync commands)")

	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newDoctorCmd(&flags))
	rootCmd.AddCommand(newAuthCmd(&flags))
	rootCmd.AddCommand(newSyncCmd(&flags))
	rootCmd.AddCommand(newListenCmd(&flags))
	rootCmd.AddCommand(newMessagesCmd(&flags))
	rootCmd.AddCommand(newSendCmd(&flags))
	rootCmd.AddCommand(newMediaCmd(&flags))
	rootCmd.AddCommand(newContactsCmd(&flags))
	rootCmd.AddCommand(newChatsCmd(&flags))
	rootCmd.AddCommand(newGroupsCmd(&flags))
	rootCmd.AddCommand(newHistoryCmd(&flags))
	rootCmd.AddCommand(newServeCmd(&flags))

	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		_ = out.WriteError(os.Stderr, flags.asJSON, err)
		return err
	}
	return nil
}

func resolveStoreDir(flags *rootFlags) string {
	storeDir := flags.storeDir
	if storeDir == "" {
		storeDir = config.DefaultStoreDir()
	}
	storeDir, _ = filepath.Abs(storeDir)
	return storeDir
}

func newApp(ctx context.Context, flags *rootFlags, needLock bool, allowUnauthed bool) (*app.App, *lock.Lock, error) {
	storeDir := resolveStoreDir(flags)

	var lk *lock.Lock
	if needLock {
		var err error
		lk, err = lock.Acquire(storeDir)
		if err != nil {
			return nil, nil, err
		}
	}

	a, err := app.New(app.Options{
		StoreDir:      storeDir,
		Version:       version,
		JSON:          flags.asJSON,
		AllowUnauthed: allowUnauthed,
	})
	if err != nil {
		if lk != nil {
			_ = lk.Release()
		}
		return nil, nil, err
	}

	return a, lk, nil
}

func withTimeout(ctx context.Context, flags *rootFlags) (context.Context, context.CancelFunc) {
	if flags.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, flags.timeout)
}

func closeApp(a *app.App, lk *lock.Lock) {
	if a != nil {
		a.Close()
	}
	if lk != nil {
		_ = lk.Release()
	}
}

func wrapErr(err error, msg string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("%s: %w", msg, err)
}
