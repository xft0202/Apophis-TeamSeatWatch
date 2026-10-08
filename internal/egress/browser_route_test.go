package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserRouteUsesReservedDirectDialAndClosesTunnels(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy authorization reached origin")
		}
		_, _ = io.WriteString(w, "platform")
	}))
	defer origin.Close()
	address := strings.TrimPrefix(origin.URL, "https://")
	source := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != "chatgpt.com:443" {
			t.Errorf("unexpected destination %s", target)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}}
	route, err := StartBrowserRoute(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer route.Close()
	proxyURL, _ := url.Parse(route.URL())
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: origin.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	// The fixture certificate is valid for example.com rather than chatgpt.com.
	transport.TLSClientConfig.ServerName = "example.com"
	client := &http.Client{Transport: transport, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get("https://chatgpt.com/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "platform" {
		t.Fatal("browser did not use reserved direct route")
	}
	route.Close()
	if _, err := client.Get("https://chatgpt.com/"); err == nil {
		t.Fatal("closed browser route retained an active connection")
	}
}

func TestBrowserRouteUsesAuthenticatedProxyAndNeverFallsBack(t *testing.T) {
	var accept atomic.Bool
	accept.Store(true)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "proxy-platform") }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && req.URL.Path == "/echo" {
			_, _ = io.WriteString(w, "1.1.1.1")
			return
		}
		if req.Method != http.MethodConnect || req.Host != "chatgpt.com:443" || req.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:secret")) {
			t.Error("reserved proxy authentication or destination changed")
		}
		if !accept.Load() {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		local, buffers, _ := w.(http.Hijacker).Hijack()
		defer local.Close()
		remote, err := net.Dial("tcp", strings.TrimPrefix(origin.URL, "https://"))
		if err != nil {
			t.Error(err)
			return
		}
		defer remote.Close()
		_, _ = buffers.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffers.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(remote, buffers); _ = remote.Close(); close(done) }()
		_, _ = io.Copy(local, remote)
		_ = local.Close()
		<-done
	}))
	defer proxy.Close()
	endpoint, _ := url.Parse(proxy.URL)
	endpoint.User = url.UserPassword("user", "secret")
	source := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(endpoint), DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}}
	echo, err := source.Get("http://fixture.test/echo")
	if err != nil || echo.StatusCode != http.StatusOK {
		t.Fatal("proxy exit check failed before browser verification")
	}
	echo.Body.Close()
	route, err := StartBrowserRoute(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer route.Close()
	localProxy, _ := url.Parse(route.URL())
	config := origin.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	config.ServerName = "example.com"
	transport := &http.Transport{Proxy: http.ProxyURL(localProxy), TLSClientConfig: config}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get("https://chatgpt.com/")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	client.CloseIdleConnections()
	accept.Store(false)
	if _, err = client.Get("https://chatgpt.com/"); err == nil {
		t.Fatal("rejected proxy silently fell back")
	}
	failure, ok := route.Failure("chatgpt.com:443")
	if !ok || failure.Code != "proxy_auth_rejected" || failure.HTTPStatus != http.StatusProxyAuthRequired {
		t.Fatalf("browser tunnel lost upstream proxy authentication rejection: %+v", failure)
	}
}

func TestBrowserRouteRejectsUntrustedDestinationAndExpiredSession(t *testing.T) {
	for _, expired := range []bool{false, true} {
		raw := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Error("denied browser request dialed a destination")
			return nil, net.ErrClosed
		}}
		var transport http.RoundTripper = raw
		if expired {
			transport = sessionTransport{next: raw, until: time.Now().Add(-time.Second)}
		}
		route, err := StartBrowserRoute(t.Context(), &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("tcp", route.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		target := "example.com:443"
		if expired {
			target = "chatgpt.com:443"
		}
		_, _ = io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusServiceUnavailable {
			t.Fatal("browser guard accepted request")
		}
		response.Body.Close()
		conn.Close()
		route.Close()
	}
}
