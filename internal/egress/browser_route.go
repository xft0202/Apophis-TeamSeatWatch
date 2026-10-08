package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// BrowserRoute lets Chromium complete first-party authentication on
// the already leased route. It owns no capacity and never selects another exit.
type BrowserRoute struct {
	listener net.Listener
	server   *http.Server
	ctx      context.Context
	route    *http.Transport
	until    time.Time
	mu       sync.Mutex
	closed   bool
	conns    map[net.Conn]struct{}
	failures map[string]ProbeStep
	stop     func() bool
}

func StartBrowserRoute(ctx context.Context, client *http.Client) (*BrowserRoute, error) {
	if ctx == nil || client == nil {
		return nil, configError("browser_route_invalid")
	}
	transport := client.Transport
	var until time.Time
	if session, ok := transport.(sessionTransport); ok {
		transport, until = session.next, session.until
	}
	raw, ok := transport.(*http.Transport)
	if !ok || raw.DialContext == nil {
		return nil, configError("browser_route_invalid")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, configError("browser_route_unavailable")
	}
	r := &BrowserRoute{listener: listener, ctx: ctx, route: raw, until: until, conns: make(map[net.Conn]struct{})}
	r.server = &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8 << 10}
	r.mu.Lock()
	r.stop = context.AfterFunc(ctx, r.Close)
	r.mu.Unlock()
	go func() { _ = r.server.Serve(listener) }()
	return r, nil
}

func (r *BrowserRoute) URL() string { return "http://" + r.listener.Addr().String() }

// Failure preserves only classified facts from a destination's latest tunnel
// failure; proxy addresses, credentials and raw dial errors remain private.
func (r *BrowserRoute) Failure(address string) (ProbeStep, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	step, ok := r.failures[address]
	return step, ok
}

func (r *BrowserRoute) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	for conn := range r.conns {
		_ = conn.Close()
	}
	r.mu.Unlock()
	_ = r.server.Close()
	if r.stop != nil {
		r.stop()
	}
}

func browserDestination(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return false
	}
	switch host {
	case "chatgpt.com", "auth.openai.com", "cdn.oaistatic.com", "auth-cdn.oaistatic.com", "challenges.cloudflare.com", "sentinel.openai.com":
		return true
	default:
		return false
	}
}

func (r *BrowserRoute) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodConnect || !browserDestination(request.Host) {
		http.Error(w, "Browser destination denied", http.StatusForbidden)
		return
	}
	if !r.until.IsZero() && !r.until.After(time.Now()) {
		http.Error(w, "Browser route expired", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	remote, err := r.dial(ctx, request.Host)
	cancel()
	r.mu.Lock()
	if err != nil {
		if r.failures == nil {
			r.failures = make(map[string]ProbeStep)
		}
		code, status := diagnosticCode(err)
		r.failures[request.Host] = ProbeStep{Stage: "platform", Code: code, HTTPStatus: status}
	} else {
		delete(r.failures, request.Host)
	}
	r.mu.Unlock()
	if err != nil {
		http.Error(w, "Browser route unavailable", http.StatusBadGateway)
		return
	}
	defer remote.Close()
	local, buffers, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer local.Close()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.conns[local], r.conns[remote] = struct{}{}, struct{}{}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.conns, local)
		delete(r.conns, remote)
		r.mu.Unlock()
	}()
	if !r.until.IsZero() {
		_ = local.SetDeadline(r.until)
		_ = remote.SetDeadline(r.until)
	}
	if _, err = buffers.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || buffers.Flush() != nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(remote, buffers); _ = remote.Close(); close(done) }()
	_, _ = io.Copy(local, remote)
	_ = local.Close()
	<-done
}

func (r *BrowserRoute) dial(ctx context.Context, address string) (net.Conn, error) {
	if r.route.Proxy == nil {
		return r.route.DialContext(ctx, "tcp", address)
	}
	endpoint, err := r.route.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: address}})
	if err != nil || endpoint == nil {
		return nil, errors.New("browser proxy route invalid")
	}
	conn, err := r.route.DialContext(ctx, "tcp", endpoint.Host)
	if err != nil {
		return nil, err
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	if endpoint.Scheme == "https" {
		config := &tls.Config{MinVersion: tls.VersionTLS12}
		if r.route.TLSClientConfig != nil {
			config = r.route.TLSClientConfig.Clone()
		}
		config.ServerName = endpoint.Hostname()
		secure := tls.Client(conn, config)
		if err = secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secure
	}
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if endpoint.User != nil {
		password, _ := endpoint.User.Password()
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(endpoint.User.Username()+":"+password)))
	}
	if err = request.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, &probeHTTPError{status: response.StatusCode}
	}
	_ = conn.SetDeadline(time.Time{})
	success = true
	return &browserTunnel{Conn: conn, reader: reader}, nil
}

type browserTunnel struct {
	net.Conn
	reader *bufio.Reader
}

func (c *browserTunnel) Read(p []byte) (int, error) { return c.reader.Read(p) }
