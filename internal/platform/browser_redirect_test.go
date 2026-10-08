package platform

import (
	"github.com/chromedp/cdproto/network"
	"testing"
)

func TestBrowserFetchUsesOriginalRedirectReceipt(t *testing.T) {
	receipt := browserFetchReceipt{request: "wanted", responses: map[network.RequestID]network.EventResponseReceivedExtraInfo{
		"unrelated": {StatusCode: 302, Headers: network.Headers{"Location": "https://other.example/"}},
		"wanted":    {StatusCode: 302, Headers: network.Headers{"location": "/continue?private=opaque"}},
	}}
	response := receipt.redirect()
	if response == nil || response.StatusCode != 302 || response.Header.Get("Location") != "/continue?private=opaque" {
		t.Fatal("lost redirect or used another request's receipt")
	}
	receipt.request = "missing"
	if receipt.redirect() != nil {
		t.Fatal("fabricated a missing receipt")
	}
}
