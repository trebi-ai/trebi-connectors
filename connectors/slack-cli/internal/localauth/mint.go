package localauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

var apiTokenRE = regexp.MustCompile(`"api_token":"(xoxc-[^"]+)"`)

// mintWeb mints a token from https://<domain>.slack.com.
func mintWeb(ctx context.Context, domain, cookie string) (string, error) {
	return Mint(ctx, http.DefaultClient, "https://"+domain+".slack.com", cookie)
}

// Mint gets a fresh xoxc- token from the workspace web page with the "d"
// cookie. baseURL is https://<domain>.slack.com.
func Mint(ctx context.Context, hc *http.Client, baseURL, cookie string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return "", err
	}
	req.AddCookie(&http.Cookie{Name: "d", Value: cookie})
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mint token: %s returned %d", baseURL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	m := apiTokenRE.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("mint token: no api_token on %s (is the session signed in?)", baseURL)
	}
	return string(m[1]), nil
}
