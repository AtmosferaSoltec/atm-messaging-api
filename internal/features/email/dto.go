package email

import "time"

// SendRequest is the body of POST /v1/email.
type SendRequest struct {
	To             string `json:"to"`
	Template       string `json:"template"`
	Data           Data   `json:"data"`
	IdempotencyKey string `json:"idempotency_key"`
}

// SendResponse is the body of a 202 answer.
type SendResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Response is the body of GET /v1/email/:id.
type Response struct {
	ID        string     `json:"id"`
	To        string     `json:"to"`
	Template  string     `json:"template"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"last_error"`
	CreatedAt time.Time  `json:"created_at"`
	SentAt    *time.Time `json:"sent_at"`
}

// NewResponse builds the public representation of an email.
func NewResponse(e Email) Response {
	return Response{
		ID: e.ID.String(), To: e.To, Template: e.Template, Status: string(e.Status),
		Attempts: e.Attempts, LastError: e.LastError, CreatedAt: e.CreatedAt, SentAt: e.SentAt,
	}
}

// TenantMetrics is one row of the admin metrics.
type TenantMetrics struct {
	TenantID   string `json:"tenant_id"`
	SystemCode string `json:"system_code"`
	Queued     int    `json:"queued"`
	Sending    int    `json:"sending"`
	Sent       int    `json:"sent"`
	Failed     int    `json:"failed"`
}

// MetricsResponse is the body of GET /admin/v1/email/metrics.
type MetricsResponse struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Window      string          `json:"window"`
	Tenants     []TenantMetrics `json:"tenants"`
}
