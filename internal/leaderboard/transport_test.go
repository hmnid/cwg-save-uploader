package leaderboard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientUsesEnvironmentProxyByDefault(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("HTTPS_PROXY", "http://environment.invalid:8080")
	client, err := NewHTTPClient("")

	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	transport := client.Transport.(*http.Transport)
	proxy, proxyErr := transport.Proxy(httptest.NewRequest(http.MethodGet, "https://example.invalid/", nil))
	if proxyErr != nil || proxy == nil || proxy.String() != "http://environment.invalid:8080" {
		t.Fatalf("environment proxy not selected: %v, %v", proxy, proxyErr)
	}
	if client.Timeout != 30*time.Second {
		t.Fatal("upload timeout changed")
	}
}

func TestHTTPClientDirectWithoutEnvironmentDisablesProxy(t *testing.T) {
	clearProxyEnvironment(t)
	client, err := NewHTTPClient("direct")

	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	proxy, proxyErr := client.Transport.(*http.Transport).Proxy(httptest.NewRequest(http.MethodGet, "https://example.invalid/", nil))
	if proxyErr != nil || proxy != nil {
		t.Fatalf("direct mode unexpectedly selected a proxy: %v, %v", proxy, proxyErr)
	}
}

func TestHTTPClientSupportsExplicitProxySchemes(t *testing.T) {
	clearProxyEnvironment(t)
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			address := scheme + "://user:secret@127.0.0.1:1080"
			request := httptest.NewRequest(http.MethodGet, "https://example.invalid/", nil)

			client, err := NewHTTPClient(address)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			proxy, proxyErr := client.Transport.(*http.Transport).Proxy(request)

			if proxyErr != nil {
				t.Fatal(proxyErr)
			}
			if proxy.String() != address {
				t.Fatal("configured proxy changed")
			}
		})
	}
}

func TestHTTPClientProxyPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		config      string
		environment map[string]string
		destination string
		want        string
	}{
		{"HTTPS environment wins", "http://config.invalid:8080", map[string]string{"HTTPS_PROXY": "http://environment.invalid:9000"}, "https://example.invalid/", "http://environment.invalid:9000"},
		{"HTTP environment wins", "http://config.invalid:8080", map[string]string{"HTTP_PROXY": "http://environment.invalid:9000"}, "http://example.invalid/", "http://environment.invalid:9000"},
		{"direct overridden by environment", "direct", map[string]string{"HTTPS_PROXY": "http://environment.invalid:9000"}, "https://example.invalid/", "http://environment.invalid:9000"},
		{"fallback per scheme", "socks5://config.invalid:1080", map[string]string{"HTTP_PROXY": "http://environment.invalid:9000"}, "https://example.invalid/", "socks5://config.invalid:1080"},
		{"NO_PROXY bypasses config", "http://config.invalid:8080", map[string]string{"NO_PROXY": "example.invalid"}, "https://example.invalid/", ""},
		{"NO_PROXY bypasses environment", "http://config.invalid:8080", map[string]string{"HTTPS_PROXY": "http://environment.invalid:9000", "NO_PROXY": "example.invalid"}, "https://example.invalid/", ""},
		{"NO_PROXY subdomain", "http://config.invalid:8080", map[string]string{"NO_PROXY": "example.invalid"}, "https://sub.example.invalid/", ""},
		{"NO_PROXY CIDR", "http://config.invalid:8080", map[string]string{"NO_PROXY": "10.0.0.0/8"}, "https://10.20.30.40/", ""},
		{"NO_PROXY wildcard", "http://config.invalid:8080", map[string]string{"NO_PROXY": "*"}, "https://example.invalid/", ""},
		{"lowercase environment", "http://config.invalid:8080", map[string]string{"https_proxy": "http://environment.invalid:9000"}, "https://example.invalid/", "http://environment.invalid:9000"},
		{"loopback bypasses config", "http://config.invalid:8080", nil, "https://127.0.0.1/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearProxyEnvironment(t)
			for name, value := range tc.environment {
				t.Setenv(name, value)
			}
			request := httptest.NewRequest(http.MethodGet, tc.destination, nil)
			client, err := NewHTTPClient(tc.config)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()

			proxy, proxyErr := client.Transport.(*http.Transport).Proxy(request)

			if proxyErr != nil {
				t.Fatal(proxyErr)
			}
			var got string
			if proxy != nil {
				got = proxy.String()
			}
			if got != tc.want {
				t.Fatalf("proxy = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestHTTPClientRejectsInvalidProxyURLs(t *testing.T) {
	clearProxyEnvironment(t)
	cases := []string{
		"localhost:8080", "ftp://localhost:8080", "http://", "http://localhost:0",
		"http://localhost:65536", "http://localhost:", "http://localhost/path",
		"http://localhost?query=yes", "http://localhost#fragment", "http://user:secret@%zz",
	}
	for _, address := range cases {
		t.Run(address, func(t *testing.T) {
			_, err := NewHTTPClient(address)

			if err == nil {
				t.Fatal("invalid proxy accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("proxy credentials exposed in error")
			}
		})
	}
}

func TestHTTPClientRoutesThroughAuthenticatedProxy(t *testing.T) {
	clearProxyEnvironment(t)
	var destination, authorization string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destination, authorization = r.URL.String(), r.Header.Get("Proxy-Authorization")
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	proxyURL.User = url.UserPassword("user", "pass")
	client, err := NewHTTPClient(proxyURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()

	response, requestErr := client.Get("http://example.invalid/resource")
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()

	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "proxied" || destination != "http://example.invalid/resource" {
		t.Fatal("request did not use configured proxy")
	}
	if authorization != "Basic dXNlcjpwYXNz" {
		t.Fatal("proxy authentication missing")
	}
}

func TestHTTPClientTunnelsHTTPSWithConnect(t *testing.T) {
	clearProxyEnvironment(t)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure response")
	}))
	defer target.Close()
	connections := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections <- r.Method + " " + r.Host
		upstream, err := net.DialTimeout("tcp", target.Listener.Addr().String(), time.Second)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		downstream, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer downstream.Close()
		_, _ = fmt.Fprint(downstream, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, downstream); upstream.Close() }()
		_, _ = io.Copy(downstream, upstream)
	}))
	defer proxy.Close()
	client, err := NewHTTPClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "127.0.0.1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	destination := fmt.Sprintf("secure.example.invalid:%d", target.Listener.Addr().(*net.TCPAddr).Port)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+destination, nil)

	response, requestErr := client.Do(request)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()

	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "secure response" {
		t.Fatal("HTTPS response not forwarded")
	}
	select {
	case connection := <-connections:
		if connection != "CONNECT "+destination {
			t.Fatalf("unexpected tunnel: %s", connection)
		}
	default:
		t.Fatal("CONNECT was not used")
	}
}

func TestHTTPClientDoesNotFollowRedirects(t *testing.T) {
	clearProxyEnvironment(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/", http.StatusFound)
	}))
	defer server.Close()
	client, err := NewHTTPClient("direct")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()

	response, requestErr := client.Get(server.URL)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	response.Body.Close()

	if response.StatusCode != http.StatusFound {
		t.Fatal("redirect policy changed")
	}
}

func clearProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(name, "")
	}
}
