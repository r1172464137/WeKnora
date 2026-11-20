package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/storageurl"
)

// ImageModeEmbed uploads reply images to DingTalk and references them by
// media_id, so no public APP_EXTERNAL_URL is needed. Any other value keeps the
// default link behaviour.
const ImageModeEmbed = "embed"

// oapiBaseURL hosts the legacy media upload API. Overridable in tests.
var oapiBaseURL = "https://oapi.dingtalk.com"

// mediaTTL bounds how long an uploaded media_id is reused. DingTalk keeps
// uploaded media for a limited time; re-uploading daily is cheap.
const mediaTTL = 24 * time.Hour

type mediaEntry struct {
	id  string
	exp time.Time
}

var markdownImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)

// SetImageMode selects how reply images are delivered ("embed" or link).
func (a *Adapter) SetImageMode(mode string) {
	a.imageMode = strings.ToLower(strings.TrimSpace(mode))
}

// InlineImages replaces every markdown image that still points at internal
// WeKnora storage with an uploaded DingTalk media_id. HTTP images are left
// alone (DingTalk can fetch them). An image that cannot be loaded or uploaded
// degrades to its alt text so the rest of the answer is still delivered.
func (a *Adapter) InlineImages(ctx context.Context, content string, load im.ImageLoader) string {
	if a.imageMode != ImageModeEmbed || load == nil {
		return content
	}
	return markdownImageRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := markdownImageRe.FindStringSubmatch(m)
		alt, ref := sub[1], sub[2]
		if storageurl.IsHTTPURL(ref) || !storageurl.Pattern.MatchString(ref) {
			return m
		}
		mediaID, err := a.mediaIDFor(ctx, ref, load)
		if err != nil {
			logger.Warnf(ctx, "[DingTalk] inline image failed, sending alt text: src=%s err=%v", ref, err)
			return imagePlaceholder(alt)
		}
		return "![" + alt + "](" + mediaID + ")"
	})
}

func imagePlaceholder(alt string) string {
	if strings.TrimSpace(alt) == "" {
		return "［图片］"
	}
	return "［图片：" + alt + "］"
}

func (a *Adapter) mediaIDFor(ctx context.Context, ref string, load im.ImageLoader) (string, error) {
	a.mediaMu.Lock()
	if e, ok := a.mediaCache[ref]; ok && time.Now().Before(e.exp) {
		a.mediaMu.Unlock()
		return e.id, nil
	}
	a.mediaMu.Unlock()

	data, err := load(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("load: %w", err)
	}
	id, err := a.uploadImage(ctx, data)
	if err != nil {
		return "", err
	}

	a.mediaMu.Lock()
	if a.mediaCache == nil {
		a.mediaCache = make(map[string]mediaEntry)
	}
	a.mediaCache[ref] = mediaEntry{id: id, exp: time.Now().Add(mediaTTL)}
	a.mediaMu.Unlock()
	return id, nil
}

// uploadImage posts one image to DingTalk media/upload and returns its media_id.
func (a *Adapter) uploadImage(ctx context.Context, data []byte) (string, error) {
	token, err := a.getAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}
	ctype := http.DetectContentType(data)
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/bmp": ".bmp"}[ctype]
	if ext == "" {
		return "", fmt.Errorf("unsupported image type %s", ctype)
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("media", "image"+ext)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	u := oapiBaseURL + "/media/upload?type=image&access_token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		MediaID string `json:"media_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode upload response (HTTP %d): %w", resp.StatusCode, err)
	}
	if out.ErrCode != 0 || out.MediaID == "" {
		return "", fmt.Errorf("upload rejected: errcode=%d errmsg=%s", out.ErrCode, out.ErrMsg)
	}
	return out.MediaID, nil
}
