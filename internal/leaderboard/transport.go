package leaderboard

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// NewHTTPClient prefers environment proxies per request scheme, then proxyURL.
// NO_PROXY applies to both sources; "direct" disables only the config fallback.
func NewHTTPClient(proxyURL string) (*http.Client, error) {
	settings := httpproxy.FromEnvironment()
	fallback := strings.TrimSpace(proxyURL)
	if fallback != "" && fallback != "direct" {
		proxy, err := parseProxyURL(fallback)
		if err != nil {
			return nil, err
		}
		if settings.HTTPProxy == "" {
			settings.HTTPProxy = proxy.String()
		}
		if settings.HTTPSProxy == "" {
			settings.HTTPSProxy = proxy.String()
		}
	}
	lookup := settings.ProxyFunc()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(request *http.Request) (*url.URL, error) {
		return lookup(request.URL)
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func parseProxyURL(raw string) (*url.URL, error) {
	// Avoid including the supplied URL in errors: it may contain credentials.
	proxy, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid proxy_url; expected a proxy URL or direct")
	}
	switch proxy.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("proxy_url scheme must be http, https, socks5, or socks5h")
	}
	if proxy.Hostname() == "" || proxy.Opaque != "" || proxy.RawQuery != "" || proxy.ForceQuery || proxy.Fragment != "" || (proxy.Path != "" && proxy.Path != "/") {
		return nil, errors.New("proxy_url must contain a host and no path, query, or fragment")
	}
	if port := proxy.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("proxy_url port must be between 1 and 65535")
		}
	} else if strings.HasSuffix(proxy.Host, ":") {
		return nil, errors.New("proxy_url port must not be empty")
	}
	return proxy, nil
}
