package store

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ParseChannelReference(input string) (kind, value string, err error) {
	v := strings.TrimSpace(input)
	invalid := func() (string, string, error) {
		return "", "", Invalid("channel_name", "invalid_channel_name", "invalid channel name")
	}
	if v == "" || utf8.RuneCountInString(v) > 512 || strings.ContainsFunc(v, unicode.IsControl) {
		return invalid()
	}
	if id, e := strconv.ParseInt(v, 10, 64); e == nil && id != 0 {
		return "id", strconv.FormatInt(id, 10), nil
	}
	if strings.HasPrefix(v, "@") {
		v = "https://max.ru/" + strings.TrimPrefix(v, "@")
	}
	if strings.HasPrefix(strings.ToLower(v), "https://") || strings.HasPrefix(strings.ToLower(v), "http://") {
		u, e := url.Parse(v)
		if e != nil || !strings.EqualFold(u.Host, "max.ru") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" || strings.ContainsFunc(u.Path, unicode.IsSpace) {
			return invalid()
		}
		return "link", "https://max.ru/" + strings.Trim(u.EscapedPath(), "/"), nil
	}
	return "title", v, nil
}
