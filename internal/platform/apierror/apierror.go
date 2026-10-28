// Package apierror defines the JSON error format returned by every endpoint.
package apierror

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Error is an error that maps to an HTTP status and a stable machine-readable code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Body is the JSON envelope of an error response.
type Body struct {
	Error Detail `json:"error"`
}

// Detail is the content of Body.
type Detail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// New builds an Error.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// BadRequest builds a 400 error.
func BadRequest(code, message string) *Error { return New(http.StatusBadRequest, code, message) }

// Unauthorized builds a 401 error.
func Unauthorized(message string) *Error {
	return New(http.StatusUnauthorized, "unauthorized", message)
}

// NotFound builds a 404 error.
func NotFound(code, message string) *Error { return New(http.StatusNotFound, code, message) }

// Conflict builds a 409 error.
func Conflict(code, message string) *Error { return New(http.StatusConflict, code, message) }

// Write sends err as the JSON response. Unknown errors become a generic 500 and
// are logged; their text is never exposed to the client.
func Write(c *gin.Context, err error) {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		c.AbortWithStatusJSON(apiErr.Status, Body{Error: Detail{Code: apiErr.Code, Message: apiErr.Message}})
		return
	}
	slog.Error("unhandled error", "error", err, "request_id", c.GetString("request_id"))
	c.AbortWithStatusJSON(http.StatusInternalServerError, Body{Error: Detail{Code: "internal_error", Message: "internal server error"}})
}
