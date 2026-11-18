package domain

import "time"

type Incoming struct {
	Type       string          `json:"type"`
	BotID      string          `json:"bot_id"`
	UserID     string          `json:"user_id"`
	Name       string          `json:"name"`
	Username   string          `json:"username"`
	ExternalID string          `json:"external_id"`
	Text       string          `json:"text"`
	At         time.Time       `json:"at"`
	Media      []IncomingMedia `json:"media"`
}
type IncomingMedia struct {
	Type, Name, URL, Token string
	Size                   int64
}
