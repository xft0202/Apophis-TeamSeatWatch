package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
)

// PersonalBrowserFactory owns one isolated browser for the whole Personal login
// attempt. Its network is bound to the caller's existing egress lease.
type PersonalBrowserFactory func(context.Context, *http.Client, http.CookieJar) (http.RoundTripper, func(), error)

type personalBrowserTransport struct {
	browser              context.Context
	jar                  http.CookieJar
	mu                   sync.Mutex
	choice               atomic.Pointer[personalChoice]
	validateContinuation func(*http.Request) error
	terminalURL          func() string
}

func NewPersonalBrowser(ctx context.Context, source *http.Client, jar http.CookieJar) (http.RoundTripper, func(), error) {
	if jar == nil {
		return nil, nil, errors.New("personal browser cookie jar unavailable")
	}
	transport := &personalBrowserTransport{jar: jar, validateContinuation: validatePersonalRequest}
	browser, closeBrowser, _, err := personalBrowser(ctx, source, transport.allowedRequest)
	if err != nil {
		return nil, nil, err
	}
	transport.browser = browser
	initialization, stopInitialization := context.WithTimeout(browser, 35*time.Second)
	defer stopInitialization()
	// Bootstrap before restoring a saved session, so initialization cannot perform
	// authenticated Workspace operations. All later requests retain this browser.
	for _, origin := range []string{officialChatBase + "/", officialAuthBase + "/log-in", officialChatBase + "/"} {
		response, _, navigateErr := transport.navigate(initialization, origin)
		err = navigateErr
		if err == nil {
			response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				err = &authHTTPError{status: response.StatusCode, browserChallenge: response.Header.Get("Cf-Mitigated") == "challenge"}
			}
		}
		if err != nil {
			closeBrowser()
			return nil, nil, err
		}
	}
	if err = transport.restoreCookies(initialization); err != nil {
		closeBrowser()
		return nil, nil, err
	}
	return transport, closeBrowser, nil
}

func personalBrowser(ctx context.Context, source *http.Client, allowed func(*network.Request) bool) (context.Context, func(), *egress.BrowserRoute, error) {
	route, err := egress.StartBrowserRoute(ctx, source)
	if err != nil {
		return nil, nil, nil, err
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath("/usr/bin/chromium"), chromedp.NoSandbox,
		chromedp.Flag("headless", false), chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-quic", true), chromedp.Flag("proxy-bypass-list", "<-loopback>"), chromedp.ProxyServer(route.URL()))
	allocator, closeAllocator := chromedp.NewExecAllocator(ctx, opts...)
	browser, closeBrowser := chromedp.NewContext(allocator)
	close := func() { closeBrowser(); closeAllocator(); route.Close() }
	if err = chromedp.Do(browser); err != nil {
		close()
		return nil, nil, nil, errors.New("personal browser unavailable")
	}
	events := chromedp.Events(browser, fetch.RequestPaused)
	go func() {
		for event, err := range events {
			if err != nil {
				return
			}
			if allowed(event.Request) {
				_, err = chromedp.Call(browser, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: event.RequestID})
			} else {
				_, err = chromedp.Call(browser, fetch.FailRequest, fetch.FailRequestParams{RequestID: event.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
			}
			if err != nil {
				return
			}
		}
	}()
	if _, err = chromedp.Call(browser, fetch.Enable, fetch.EnableParams{}); err != nil {
		close()
		return nil, nil, nil, errors.New("personal browser request guard unavailable")
	}
	return browser, close, route, nil
}

// Page scripts and redirects are fenced as well as explicit protocol requests.
func allowedPersonalBrowserRequest(request *network.Request) bool {
	if request == nil {
		return false
	}
	u, err := url.Parse(request.URL)
	if err != nil {
		return false
	}
	if u.Scheme == "data" || u.Scheme == "blob" {
		return true
	}
	if u.Hostname() == "chatgpt.com" || u.Hostname() == "auth.openai.com" {
		// Edge challenges may submit their own first-party verification request.
		if u.Scheme == "https" && u.Port() == "" && strings.HasPrefix(u.Path, "/cdn-cgi/") {
			return true
		}
		return validatePersonalRequest(&http.Request{Method: request.Method, URL: u}) == nil
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	switch u.Hostname() {
	case "cdn.oaistatic.com", "auth-cdn.oaistatic.com":
		return request.Method == http.MethodGet
	case "challenges.cloudflare.com", "sentinel.openai.com":
		return request.Method == http.MethodGet || request.Method == http.MethodPost
	default:
		return false
	}
}

func (t *personalBrowserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validatePersonalRequest(req); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if choice, ok := req.Context().Value(personalChoiceContextKey{}).(*personalChoice); ok && personalChoiceRequest(req) {
		t.choice.Store(choice)
		defer t.choice.Store(nil)
	}
	ctx, cancel := context.WithCancel(t.browser)
	defer cancel()
	stop := context.AfterFunc(req.Context(), cancel)
	defer stop()
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	var response *http.Response
	var err error
	if strings.HasPrefix(req.Header.Get("Accept"), "text/html") {
		response, _, err = t.navigate(ctx, req.URL.String())
	} else {
		response, err = t.fetch(ctx, req)
	}
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, err
	}
	if err = t.copyCookies(ctx); err != nil {
		response.Body.Close()
		return nil, err
	}
	return response, nil
}

func (t *personalBrowserTransport) navigate(ctx context.Context, target string) (*http.Response, string, error) {
	old, err := chromedp.Run(ctx, chromedp.Evaluate[float64](`performance.timeOrigin`))
	if err != nil {
		return nil, "", errors.New("personal browser document unavailable")
	}
	trackCtx, stopTrack := context.WithCancel(ctx)
	defer stopTrack()
	tree, err := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return nil, "", errors.New("personal browser frame unavailable")
	}
	events := chromedp.Events(trackCtx, network.ResponseReceived)
	var responseMu sync.Mutex
	var result *network.Response
	go func() {
		for event, err := range events {
			if err != nil {
				return
			}
			if event.Type == network.ResourceTypeDocument && event.FrameID == tree.FrameTree.Frame.ID && event.Response != nil {
				u, _ := url.Parse(event.Response.URL)
				if u != nil && (u.Hostname() == "chatgpt.com" || u.Hostname() == "auth.openai.com") {
					responseMu.Lock()
					result = event.Response
					responseMu.Unlock()
				}
			}
		}
	}()
	nav, err := chromedp.Call(ctx, page.Navigate, page.NavigateParams{URL: target})
	if t.terminalURL != nil && t.terminalURL() != "" {
		terminal := t.terminalURL()
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{terminal}}, Body: http.NoBody, Request: request}, terminal, nil
	}
	if err != nil || nav.ErrorText != "" {
		return nil, "", errors.New("personal browser navigation failed")
	}
	marker, _ := json.Marshal(old)
	// DOM readiness is sufficient for the fenced protocol. Waiting for every
	// third-party page resource would consume the account request timeout.
	ready := `performance.timeOrigin !== ` + string(marker) + ` && document.readyState !== 'loading' && !/^Just a moment/.test(document.title)`
	readiness, stopReadiness := context.WithTimeout(ctx, 30*time.Second)
	defer stopReadiness()
	if err = waitForBrowserDocument(readiness, func(ctx context.Context) (bool, error) {
		if t.terminalURL != nil && t.terminalURL() != "" {
			return true, nil
		}
		return chromedp.Run(ctx, chromedp.Evaluate[bool](ready))
	}); err != nil {
		responseMu.Lock()
		defer responseMu.Unlock()
		if result != nil {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			return browserDocumentResponse(result, request, http.NoBody), "", errors.New("personal browser verification did not complete")
		}
		return nil, "", errors.New("personal browser verification did not complete")
	}
	if t.terminalURL != nil {
		if terminal := t.terminalURL(); terminal != "" {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{terminal}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, terminal, nil
		}
	}
	responseMu.Lock()
	defer responseMu.Unlock()
	if result == nil {
		return nil, "", errors.New("personal browser navigation response unavailable")
	}
	page, err := chromedp.Run(ctx, chromedp.Evaluate[struct{ URL, Body string }](`({URL:location.href,Body:document.documentElement.outerHTML.slice(0,1048577)})`))
	if err != nil {
		return nil, "", errors.New("personal browser page unavailable")
	}
	final, err := http.NewRequestWithContext(ctx, http.MethodGet, page.URL, nil)
	if err != nil || t.validateContinuation(final) != nil {
		return nil, "", errors.New("personal browser continuation denied")
	}
	return browserDocumentResponse(result, final, io.NopCloser(strings.NewReader(page.Body))), page.URL, nil
}

func browserDocumentResponse(result *network.Response, request *http.Request, body io.ReadCloser) *http.Response {
	headers := make(http.Header)
	for name, value := range result.Headers {
		if s, ok := value.(string); ok {
			headers.Set(name, s)
		}
	}
	return &http.Response{StatusCode: int(result.Status), Header: headers, Body: body, Request: request}
}

func (t *personalBrowserTransport) fetch(ctx context.Context, req *http.Request) (*http.Response, error) {
	current, err := chromedp.Run(ctx, chromedp.Evaluate[string](`location.origin`))
	if err != nil {
		return nil, errors.New("personal browser origin unavailable")
	}
	origin := req.URL.Scheme + "://" + req.URL.Host
	if current != origin {
		entry := origin + "/"
		if req.URL.Hostname() == "auth.openai.com" {
			entry = origin + "/log-in"
		}
		response, _, err := t.navigate(ctx, entry)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
	}
	body := ""
	if req.Body != nil {
		data, err := io.ReadAll(io.LimitReader(req.Body, maxResponseBody+1))
		req.Body.Close()
		if err != nil || len(data) > maxResponseBody {
			return nil, errors.New("personal browser request incomplete")
		}
		body = string(data)
	}
	headers := make(map[string]string)
	for _, name := range []string{"Accept", "Content-Type"} {
		if value := req.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	args, _ := json.Marshal(struct {
		URL, Method, Body string
		Headers           map[string]string
	}{req.URL.String(), req.Method, body, headers})
	tracking, stopTracking := context.WithCancel(ctx)
	defer stopTracking()
	receipt := captureBrowserFetch(tracking, req)
	script := `(async () => {
 const p = ` + string(args) + `;
 const response = await fetch(p.URL, {
  method: p.Method, headers: p.Headers,
  body: p.Method === 'GET' ? undefined : p.Body,
  credentials: 'include', redirect: 'manual'
 });
 const reader = response.body?.getReader();
 const decoder = new TextDecoder();
 let body = '', total = 0;
 if (reader) {
  for (;;) {
   const chunk = await reader.read();
   if (chunk.done) break;
   total += chunk.value.length;
   if (total > 1048576) {
    await reader.cancel();
    throw new Error('response too large');
   }
   body += decoder.decode(chunk.value, {stream: true});
  }
  body += decoder.decode();
 }
 return {Status: response.status, Headers: Object.fromEntries(response.headers.entries()), Body: body};
})()`
	wire, err := chromedp.Run(ctx, chromedp.Evaluate[struct {
		Status  int
		Headers map[string]string
		Body    string
	}](script, chromedp.EvalAwaitPromise))
	if err != nil {
		return nil, errors.New("personal browser API request failed")
	}
	if wire.Status == 0 {
		// CDP events and the JS result arrive independently. Await only this
		// receipt; never replay the POST when the response is an opaque redirect.
		bounded, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		var redirect *http.Response
		if err = waitForBrowserDocument(bounded, func(context.Context) (bool, error) { redirect = receipt.redirect(); return redirect != nil, nil }); err != nil {
			return nil, errors.New("browser redirect receipt unavailable")
		}
		redirect.Request = req
		return redirect, nil
	}
	responseHeaders := make(http.Header)
	for name, value := range wire.Headers {
		responseHeaders.Set(name, value)
	}
	return &http.Response{StatusCode: wire.Status, Header: responseHeaders, Body: io.NopCloser(strings.NewReader(wire.Body)), Request: req}, nil
}

func (t *personalBrowserTransport) restoreCookies(ctx context.Context) error {
	cookies := []*network.CookieParam{}
	for _, host := range []string{"chatgpt.com", "auth.openai.com"} {
		origin, _ := url.Parse("https://" + host + "/")
		for _, cookie := range t.jar.Cookies(origin) {
			if cookie.Name == "__cf_bm" || cookie.Name == "cf_clearance" || cookie.Name == "_cfuvid" {
				continue
			}
			cookies = append(cookies, &network.CookieParam{Name: cookie.Name, Value: cookie.Value, URL: origin.String(), Path: "/", Secure: true, HTTPOnly: true})
		}
	}
	if len(cookies) > 0 {
		if _, err := chromedp.Call(ctx, network.SetCookies, network.SetCookiesParams{Cookies: cookies}); err != nil {
			return errors.New("personal browser session restoration failed")
		}
	}
	return nil
}

func (t *personalBrowserTransport) copyCookies(ctx context.Context) error {
	snapshot, err := chromedp.CallBrowser(ctx, storage.GetCookies, storage.GetCookiesParams{})
	if err != nil {
		return errors.New("personal browser cookies unavailable")
	}
	seedBrowserCookies(t.jar, snapshot.Cookies)
	return nil
}

func seedBrowserCookies(jar http.CookieJar, cookies []*network.Cookie) {
	for _, host := range []string{"chatgpt.com", "auth.openai.com"} {
		origin, _ := url.Parse("https://" + host + "/")
		selected := make(map[string]*network.Cookie)
		for _, cookie := range cookies {
			if cookie == nil {
				continue
			}
			domain := strings.TrimPrefix(cookie.Domain, ".")
			if domain != host && !(domain == "openai.com" && host == "auth.openai.com") {
				continue
			}
			if len(cookie.Name) > 128 || len(cookie.Value) > 16384 {
				continue
			}
			candidate := &http.Cookie{Name: cookie.Name, Value: cookie.Value}
			if candidate.Valid() != nil {
				continue
			}
			if saved := selected[cookie.Name]; saved == nil || len(domain) > len(strings.TrimPrefix(saved.Domain, ".")) {
				selected[cookie.Name] = cookie
			}
		}
		seed := make([]*http.Cookie, 0, len(selected))
		for _, cookie := range selected {
			seed = append(seed, &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: "/", Secure: true, HttpOnly: true})
		}
		jar.SetCookies(origin, seed)
	}
}

// Each redirect replaces its execution context. Re-evaluate readiness in the
// current document; never replay the navigation or a login submission.
func waitForBrowserDocument(ctx context.Context, evaluate func(context.Context) (bool, error)) error {
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := evaluate(ctx)
		if err != nil {
			var protocol *cdproto.Error
			if !errors.As(err, &protocol) || protocol.Code != -32000 || (protocol.Message != "Cannot find context with specified id" && protocol.Message != "Execution context was destroyed.") {
				return err
			}
		} else if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (t *personalBrowserTransport) allowedRequest(req *network.Request) bool {
	if allowedPersonalBrowserRequest(req) {
		return true
	}
	choice := t.choice.Load()
	if req == nil || choice == nil {
		return false
	}
	var body []byte
	for _, entry := range req.PostDataEntries {
		if entry == nil || len(body)+len(entry.Bytes) > 1024 {
			return false
		}
		body = append(body, entry.Bytes...)
	}
	request, err := http.NewRequestWithContext(context.WithValue(context.Background(), personalChoiceContextKey{}, choice), req.Method, req.URL, bytes.NewReader(body))
	return err == nil && validatePersonalRequest(request) == nil
}
