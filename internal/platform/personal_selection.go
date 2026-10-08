package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

type personalChoice struct{ id string }
type personalChoiceContextKey struct{}

// Only the current first-party authentication session can identify the Personal
// choice. A Team ID or ambiguous list never authorizes account selection.
func personalChoiceFromSession(data []byte) (*personalChoice, error) {
	var snapshot struct {
		Session struct {
			Workspaces []struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"workspaces"`
		} `json:"client_auth_session"`
	}
	if json.Unmarshal(data, &snapshot) != nil || len(snapshot.Session.Workspaces) > 1000 {
		return nil, errors.New("personal choice unavailable")
	}
	var id string
	for _, item := range snapshot.Session.Workspaces {
		if item.Kind != "personal" {
			continue
		}
		if _, err := uuid.Parse(item.ID); err != nil || strings.TrimSpace(item.ID) != item.ID || (id != "" && id != item.ID) {
			return nil, errors.New("personal choice invalid or ambiguous")
		}
		id = item.ID
	}
	if id == "" {
		return nil, errors.New("personal choice unavailable")
	}
	return &personalChoice{id: id}, nil
}

func personalChoiceRequest(req *http.Request) bool {
	if req == nil || req.URL == nil || req.Method != http.MethodPost || req.URL.String() != officialAuthBase+"/api/accounts/workspace/select" || req.GetBody == nil {
		return false
	}
	choice, ok := req.Context().Value(personalChoiceContextKey{}).(*personalChoice)
	if !ok || choice == nil || choice.id == "" {
		return false
	}
	body, err := req.GetBody()
	if err != nil {
		return false
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 1025))
	if err != nil || len(data) > 1024 {
		return false
	}
	return personalChoiceBody(data, choice)
}

func personalChoiceBody(data []byte, choice *personalChoice) bool {
	var fields map[string]string
	return choice != nil && json.Unmarshal(data, &fields) == nil && len(fields) == 1 && fields["workspace_id"] == choice.id
}

func (r *HTTPReader) finishPersonalChoice(ctx context.Context, jar http.CookieJar, landing string) error {
	u, err := url.Parse(landing)
	if err != nil {
		return errors.New("personal continuation invalid")
	}
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/workspace" {
		return nil
	}
	data, err := r.authJSONWithJar(ctx, jar, http.MethodGet, officialAuthBase+"/api/accounts/client_auth_session_dump", nil, landing)
	if err != nil {
		return err
	}
	choice, err := personalChoiceFromSession(data)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"workspace_id": choice.id})
	selected, err := r.authJSONWithJar(context.WithValue(ctx, personalChoiceContextKey{}, choice), jar, http.MethodPost, officialAuthBase+"/api/accounts/workspace/select", body, landing)
	if err != nil {
		return err
	}
	next := continueURLFromJSON(selected)
	if next == "" {
		return errors.New("personal selection continuation missing")
	}
	_, _, err = r.followBrowserRedirect(ctx, jar, next, landing)
	return err
}
