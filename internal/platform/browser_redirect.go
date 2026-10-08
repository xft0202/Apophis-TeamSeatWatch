package platform

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Browser fetch intentionally stops at redirects. JavaScript cannot read the
// opaque redirect's status or Location; CDP supplies the original HTTP receipt.
type browserFetchReceipt struct {
	mu        sync.Mutex
	request   network.RequestID
	responses map[network.RequestID]network.EventResponseReceivedExtraInfo
}

func captureBrowserFetch(ctx context.Context, req *http.Request) *browserFetchReceipt {
	receipt := &browserFetchReceipt{responses: make(map[network.RequestID]network.EventResponseReceivedExtraInfo)}
	requests := chromedp.Events(ctx, network.RequestWillBeSent)
	responses := chromedp.Events(ctx, network.ResponseReceivedExtraInfo)
	go func() {
		for event, err := range requests {
			if err != nil {
				return
			}
			if event.Request != nil && event.Request.URL == req.URL.String() && event.Request.Method == req.Method {
				receipt.mu.Lock()
				receipt.request = event.RequestID
				receipt.mu.Unlock()
			}
		}
	}()
	go func() {
		for event, err := range responses {
			if err != nil {
				return
			}
			receipt.mu.Lock()
			receipt.responses[event.RequestID] = event
			receipt.mu.Unlock()
		}
	}()
	return receipt
}
func (r *browserFetchReceipt) redirect() *http.Response {
	r.mu.Lock()
	defer r.mu.Unlock()
	event, found := r.responses[r.request]
	if !found || event.StatusCode < 300 || event.StatusCode >= 400 {
		return nil
	}
	headers := make(http.Header)
	for name, value := range event.Headers {
		if strings.EqualFold(name, "Location") {
			if location, ok := value.(string); ok {
				headers.Set("Location", location)
			}
		}
	}
	if headers.Get("Location") == "" {
		return nil
	}
	return &http.Response{StatusCode: int(event.StatusCode), Header: headers, Body: http.NoBody}
}
