package domain

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChannelReference identifies a discovered channel. Titles are exact,
// case-insensitive matches; invitation links remain case-sensitive.
type ChannelReference struct{ Kind, Value string }

func ParseChannelReference(input string) (ChannelReference, error) {
	v := strings.TrimSpace(input)
	invalid := func() (ChannelReference, error) {
		return ChannelReference{}, &RuleError{Field: "channel_name", Reason: "invalid_channel_name"}
	}
	if v == "" || utf8.RuneCountInString(v) > 512 || strings.ContainsFunc(v, unicode.IsControl) {
		return invalid()
	}
	if id, err := strconv.ParseInt(v, 10, 64); err == nil && id != 0 {
		return ChannelReference{Kind: "id", Value: strconv.FormatInt(id, 10)}, nil
	}
	if strings.HasPrefix(v, "@") {
		v = "https://max.ru/" + strings.TrimPrefix(v, "@")
	}
	if strings.HasPrefix(strings.ToLower(v), "https://") || strings.HasPrefix(strings.ToLower(v), "http://") {
		u, err := url.Parse(v)
		if err != nil || !strings.EqualFold(u.Host, "max.ru") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" || strings.ContainsFunc(u.Path, unicode.IsSpace) {
			return invalid()
		}
		return ChannelReference{Kind: "link", Value: "https://max.ru/" + strings.Trim(u.EscapedPath(), "/")}, nil
	}
	return ChannelReference{Kind: "title", Value: v}, nil
}
