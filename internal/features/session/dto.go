package session

import "time"

// CreateRequest is the body of POST /v1/sessions.
type CreateRequest struct {
	AccountRef string `json:"account_ref" binding:"required"`
	Channel    string `json:"channel" binding:"required"`
}

// Response is the public representation of a session.
type Response struct {
	ID          string     `json:"id"`
	AccountRef  string     `json:"account_ref"`
	Channel     string     `json:"channel"`
	Status      string     `json:"status"`
	ConnectedAt *time.Time `json:"connected_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// NewResponse maps a Session to its public representation.
func NewResponse(s Session) Response {
	return Response{
		ID:          s.ID.String(),
		AccountRef:  s.AccountRef,
		Channel:     string(s.Channel),
		Status:      string(s.Status),
		ConnectedAt: s.ConnectedAt,
		CreatedAt:   s.CreatedAt,
	}
}
