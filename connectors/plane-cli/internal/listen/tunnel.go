package listen

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"time"
)

var tunnelURLRE = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Tunnel is a cloudflared quick tunnel: a public trycloudflare.com URL that
// forwards to a local address. It needs no Cloudflare account.
type Tunnel struct {
	URL string
	cmd *exec.Cmd
}

// StartTunnel runs cloudflared for localURL and waits until the public URL
// answers. Plane retries a failed delivery only after about 10 minutes, so the
// URL must work before the webhook exists.
func StartTunnel(ctx context.Context, localURL string, timeout time.Duration) (*Tunnel, error) {
	bin, err := exec.LookPath("cloudflared")
	if err != nil {
		return nil, errors.New("cloudflared is not on PATH (brew install cloudflared), or pass --public-url")
	}
	cmd := exec.Command(bin, "tunnel", "--no-autoupdate", "--url", localURL)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start cloudflared: %w", err)
	}
	t := &Tunnel{cmd: cmd}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if u := tunnelURLRE.FindString(sc.Text()); u != "" {
				select {
				case found <- u:
				default:
				}
			}
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()

	deadline := time.After(timeout)
	select {
	case t.URL = <-found:
	case <-deadline:
		t.Close()
		return nil, errors.New("cloudflared did not give a tunnel URL in time")
	case <-ctx.Done():
		t.Close()
		return nil, ctx.Err()
	}
	var lastErr error
	for {
		if lastErr = reachable(ctx, t.URL); lastErr == nil {
			return t, nil
		}
		select {
		case <-deadline:
			t.Close()
			return nil, fmt.Errorf("tunnel %s did not answer in time: %w", t.URL, lastErr)
		case <-ctx.Done():
			t.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// probeClient resolves names with a public DNS server. The system resolver
// can cache "no such host" for the new name, from before the tunnel existed.
var probeClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{DialContext: (&net.Dialer{
		Resolver: &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "1.1.1.1:53")
		}},
	}).DialContext},
}

func reachable(ctx context.Context, u string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := probeClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// Close stops cloudflared.
func (t *Tunnel) Close() {
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
	}
}
