//go:build fleet

package vnc_test

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/trebi-ai/trebi-connectors/connectors/vnc-cli/vnc"
)

// Each server has BlacklistThreshold=500, so the tests below that close
// before auth do not lock the client out.

func fleetTLS(t *testing.T) *tls.Config {
	t.Helper()
	cfg, err := vnc.NewTLSConfig("../fleet/vencrypt-certs/cert.pem", false)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestFleetTigerVNCVeNCrypt(t *testing.T) {
	for name, o := range map[string]vnc.Options{
		"X509Vnc":  {Password: "vncdotool", TLS: fleetTLS(t)},
		"X509None": {TLS: fleetTLS(t)},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			o.Addr = fleetAddr("5941")
			c, err := vnc.Dial(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			waitScene(t, ctx, c, "0", 0)
		})
	}
}

func TestFleetTigerVNCNameMismatch(t *testing.T) {
	cfg := fleetTLS(t)
	cfg.ServerName = "wrong.example"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5941"), TLS: cfg})
	if err == nil || !strings.Contains(err.Error(), "does not match the server name") {
		t.Fatalf("err = %v", err)
	}
}

func TestFleetTigerVNCDefaultTypes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5942"), Password: "vncdotool"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitScene(t, ctx, c, "0", 0)
}

func TestFleetTigerVNCAnonymousOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := vnc.Dial(ctx, vnc.Options{Addr: fleetAddr("5951"), Password: "vncdotool"})
	if err == nil || !strings.Contains(err.Error(), "anonymous TLS is not supported") {
		t.Fatalf("err = %v", err)
	}
	t.Log(err)
}
