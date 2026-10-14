package labtest

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// The Dex user for SSO logins (lab/dex/config.yaml).
const (
	SSOUser     = "sso@example.com"
	SSOPassword = "lab"
)

// SSOLogin completes a headscale login URL as the lab's Dex user, the way a browser
// would: Dex's password form, then headscale's "Confirm registration" form. It needs
// no browser, so it also works on machines without one.
func SSOLogin(t testing.TB, loginURL string) {
	t.Helper()
	if err := ssoLogin(loginURL); err != nil {
		t.Fatalf("SSO login: %v", err)
	}
}

func ssoLogin(loginURL string) error {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: httpc.Timeout}

	// headscale redirects to Dex, which shows its password form right away because the
	// password database is its only connector.
	resp, err := c.Get(loginURL)
	if err != nil {
		return err
	}
	f, err := readForm(resp)
	if err != nil {
		return fmt.Errorf("Dex login page: %w", err)
	}
	f.values.Set("login", SSOUser)
	f.values.Set("password", SSOPassword)

	// Dex redirects back to headscale, which asks to confirm the registration.
	resp, err = c.PostForm(f.action, f.values)
	if err != nil {
		return err
	}
	f, err = readForm(resp)
	if err != nil {
		return fmt.Errorf("headscale confirmation page: %w", err)
	}
	if !strings.Contains(f.action, "/register/confirm/") {
		return fmt.Errorf("expected headscale's confirmation form, got a form for %s", f.action)
	}
	resp, err = c.PostForm(f.action, f.values)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registration failed with %s: %s", resp.Status, b)
	}
	return nil
}

// form is the first HTML form of a page, with its action resolved and its inputs'
// default values.
type form struct {
	action string
	values url.Values
}

func readForm(resp *http.Response) (form, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return form{}, fmt.Errorf("%s: %s", resp.Status, b)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return form{}, err
	}
	fn := find(doc, func(n *html.Node) bool { return n.Data == "form" })
	if fn == nil {
		return form{}, fmt.Errorf("no form on %s", resp.Request.URL)
	}
	action, err := resp.Request.URL.Parse(attr(fn, "action"))
	if err != nil {
		return form{}, err
	}
	f := form{action: action.String(), values: url.Values{}}
	find(fn, func(n *html.Node) bool {
		if (n.Data == "input" || n.Data == "button") && attr(n, "name") != "" {
			f.values.Set(attr(n, "name"), attr(n, "value"))
		}
		return false
	})
	return f, nil
}

// find returns the first element below n (depth first) for which match is true.
func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && match(c) {
			return c
		}
		if m := find(c, match); m != nil {
			return m
		}
	}
	return nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
