package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 1x1 PNG.
var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

func TestInlineImages(t *testing.T) {
	useTestHTTPClient(t)
	uploads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"accessToken": "tok", "expireIn": 7200})
		case "/media/upload":
			uploads++
			if r.URL.Query().Get("access_token") != "tok" || r.URL.Query().Get("type") != "image" {
				t.Errorf("bad upload query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"errcode": 0, "media_id": "@lADmedia"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	origAPI, origOAPI := apiBaseURL, oapiBaseURL
	apiBaseURL, oapiBaseURL = srv.URL, srv.URL
	defer func() { apiBaseURL, oapiBaseURL = origAPI, origOAPI }()

	load := func(_ context.Context, ref string) ([]byte, error) {
		if ref == "resource://missing" {
			return nil, errors.New("gone")
		}
		return tinyPNG, nil
	}
	content := "a ![面板](resource://abc) b ![面板](resource://abc) c ![x](https://h/x.png) d ![坏图](resource://missing)"

	a := &Adapter{clientID: "cid", clientSecret: "sec"}
	if got := a.InlineImages(context.Background(), content, load); got != content {
		t.Fatalf("link mode must not change content, got %q", got)
	}

	a.SetImageMode(" Embed ")
	got := a.InlineImages(context.Background(), content, load)
	want := "a ![面板](@lADmedia) b ![面板](@lADmedia) c ![x](https://h/x.png) d ［图片：坏图］"
	if got != want {
		t.Fatalf("embed mode:\n got  %q\n want %q", got, want)
	}
	if uploads != 1 {
		t.Fatalf("same ref should upload once, uploaded %d times", uploads)
	}
	if strings.Contains(got, "resource://") {
		t.Fatalf("internal ref leaked: %q", got)
	}
}
