package max

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/leha-creator/chanly/internal/platform/outbound"
)

type SupportMedia struct{ Type, Token string }
type SupportDeliveryError struct {
	Reason                string
	Definitive, Temporary bool
}

func (e *SupportDeliveryError) Error() string { return "MAX support: " + e.Reason }

func supportOutcome(resp outbound.Response, err error) error {
	var api struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(resp.Body, &api)
	if api.Code == "attachment.not.ready" {
		return &SupportDeliveryError{"attachment_not_ready", true, true}
	}
	if err == nil {
		return nil
	}
	if resp.Status == http.StatusTooManyRequests {
		return &SupportDeliveryError{"rate_limited", true, true}
	}
	if resp.Status >= 400 && resp.Status < 500 {
		return &SupportDeliveryError{"rejected_" + strconv.Itoa(resp.Status), true, false}
	}
	if errors.Is(err, outbound.ErrCircuitOpen) {
		return &SupportDeliveryError{"circuit_open", true, true}
	}
	return &SupportDeliveryError{"outcome_unknown", false, false}
}

func (c *Client) SendSupportMessage(ctx context.Context, userID, text string, media []SupportMedia) (string, error) {
	id, e := strconv.ParseInt(userID, 10, 64)
	if e != nil || id <= 0 {
		return "", &SupportDeliveryError{"invalid_recipient", true, false}
	}
	for _, b := range []struct{ chat bool }{{false}, {true}} {
		bucket := c.globalBucket()
		if b.chat {
			bucket = c.chatBucket("user", id)
		}
		if e = c.limiter.Wait(ctx, bucket, priority(ctx)); e != nil {
			return "", &SupportDeliveryError{"rate_limited", true, true}
		}
	}
	attachments := make([]map[string]any, 0, len(media))
	for _, m := range media {
		attachments = append(attachments, map[string]any{"type": m.Type, "payload": map[string]string{"token": m.Token}})
	}
	resp, e := c.out.Do(ctx, outbound.Call{Operation: "SendSupportMessage", Method: http.MethodPost, URL: c.opts.BaseURL + "/messages?user_id=" + url.QueryEscape(userID), Header: http.Header{"Authorization": {c.opts.Token}}, Body: struct {
		Text        string           `json:"text,omitempty"`
		Attachments []map[string]any `json:"attachments,omitempty"`
	}{text, attachments}, Sensitive: true, NoRetry: true, Create: true})
	if err := supportOutcome(resp, e); err != nil {
		return "", err
	}
	var result sentMessage
	if json.Unmarshal(resp.Body, &result) != nil || result.Message.Body.MID == "" {
		return "", &SupportDeliveryError{"missing_message_id", false, false}
	}
	return result.Message.Body.MID, nil
}

func (c *Client) UploadSupportMedia(ctx context.Context, kind, name string, r io.Reader) (string, error) {
	if e := c.limiter.Wait(ctx, c.globalBucket(), priority(ctx)); e != nil {
		return "", errors.New("support media rate limited")
	}
	resp, e := c.out.Do(ctx, outbound.Call{Operation: "SupportUploadSlot", Method: http.MethodPost, URL: c.opts.BaseURL + "/uploads?type=" + url.QueryEscape(kind), Header: http.Header{"Authorization": {c.opts.Token}}, Sensitive: true, NoRetry: true})
	if failure := supportUploadOutcome(resp, e); failure != nil {
		return "", failure
	}
	var slot struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if json.Unmarshal(resp.Body, &slot) != nil || slot.URL == "" {
		return "", &SupportDeliveryError{"upload_invalid_slot", true, false}
	}
	if e = outbound.PublicMediaURL(slot.URL, c.opts.MediaHosts); e != nil {
		return "", &SupportDeliveryError{"upload_origin_not_allowed", true, false}
	}
	file, e := os.CreateTemp("", "support-upload-*")
	if e != nil {
		return "", errors.New("support upload staging failed")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	mw := multipart.NewWriter(file)
	part, source, e := supportUploadPart(mw, name, r)
	if e != nil {
		return "", e
	}
	n, e := io.Copy(part, io.LimitReader(source, (250<<20)+1))
	if e != nil || n > 250<<20 {
		return "", errors.New("support media copy failed")
	}
	if e = mw.Close(); e != nil {
		return "", e
	}
	size, e := file.Seek(0, io.SeekEnd)
	if e != nil {
		return "", e
	}
	if _, e = file.Seek(0, io.SeekStart); e != nil {
		return "", e
	}
	c.log.DebugContext(ctx, "[FIX] support upload prepared", "type", kind, "bytes", n)
	// The signed CDN slot authenticates itself; never forward the bot token to it.
	if e = c.limiter.Wait(ctx, c.globalBucket(), priority(ctx)); e != nil {
		return "", errors.New("support media rate limited")
	}
	up, e := c.out.Do(ctx, outbound.Call{Operation: "SupportUploadFile", Method: http.MethodPost, URL: slot.URL, BodyReader: file, BodySize: size, ContentType: mw.FormDataContentType(), Sensitive: true, NoRetry: true, Upload: true})
	if failure := supportUploadOutcome(up, e); failure != nil {
		return "", failure
	}
	if slot.Token != "" {
		if !supportUploadConfirmed(up.Body) {
			return "", &SupportDeliveryError{"upload_unconfirmed", true, false}
		}
		return slot.Token, nil
	}
	token, e := uploadToken(up.Body)
	if e != nil {
		return "", &SupportDeliveryError{"upload_unconfirmed", true, false}
	}
	return token, nil
}

// Uploads precede message creation: a retry cannot duplicate a chat message.
// Permanent format/auth refusals should fail promptly and unblock later replies.
func supportUploadOutcome(response outbound.Response, err error) error {
	if response.Status >= 200 && response.Status < 300 && err == nil {
		if validateSuccess(response) == nil {
			return nil
		}
		return &SupportDeliveryError{"upload_rejected", true, false}
	}
	temporary := response.Status == 0 || response.Status == 429 || response.Status >= 500 || errors.Is(err, outbound.ErrCircuitOpen)
	reason := "upload_unavailable"
	if response.Status >= 400 && response.Status < 500 && response.Status != 429 {
		reason = "upload_rejected_" + strconv.Itoa(response.Status)
	}
	return &SupportDeliveryError{reason, true, temporary}
}

func supportUploadConfirmed(body []byte) bool {
	if strings.TrimSpace(string(body)) == "<retval>1</retval>" {
		return true
	}
	var result struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
	}
	return json.Unmarshal(body, &result) == nil && (result.Success || result.Token != "")
}

func supportUploadPart(writer *multipart.Writer, name string, source io.Reader) (io.Writer, io.Reader, error) {
	buffered := bufio.NewReader(source)
	prefix, _ := buffered.Peek(512)
	contentType := http.DetectContentType(prefix)
	// Match browser/curl multipart filenames. filename* is not supported by
	// every MAX CDN parser; preserve UTF-8 in the ordinary filename parameter.
	name = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\r", "", "\n", "").Replace(name)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="data"; filename="` + name + `"`},
		"Content-Type":        {contentType},
	})
	return part, buffered, err
}

func (c *Client) ResolveSupportVideo(ctx context.Context, token string) (string, error) {
	if e := c.limiter.Wait(ctx, c.globalBucket(), priority(ctx)); e != nil {
		return "", errors.New("support video rate limited")
	}
	resp, e := c.out.Do(ctx, outbound.Call{Operation: "SupportVideo", Method: http.MethodGet, URL: c.opts.BaseURL + "/videos/" + url.PathEscape(token), Header: http.Header{"Authorization": {c.opts.Token}}, Sensitive: true, NoRetry: true})
	if e != nil {
		return "", errors.New("support video lookup failed")
	}
	var b struct {
		URLs map[string]string `json:"urls"`
	}
	if json.Unmarshal(resp.Body, &b) != nil {
		return "", errors.New("invalid video metadata")
	}
	for _, quality := range []string{"mp4_720", "mp4_480", "mp4_360", "mp4_1080"} {
		if u := b.URLs[quality]; u != "" {
			return u, nil
		}
	}
	return "", errors.New("video source unavailable")
}
