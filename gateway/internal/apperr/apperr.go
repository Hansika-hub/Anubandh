// Package apperr implements the shared error codes (BUILD.md §12) and their HTTP mapping.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
)

// Code is one of the frozen ERR_* codes.
type Code string

// Frozen error codes (BUILD.md §12).
const (
	ErrBadRequest            Code = "ERR_BAD_REQUEST"
	ErrUnauthorized          Code = "ERR_UNAUTHORIZED"
	ErrForbiddenRole         Code = "ERR_FORBIDDEN_ROLE"
	ErrNotFound              Code = "ERR_NOT_FOUND"
	ErrInvalidTransition     Code = "ERR_INVALID_TRANSITION"
	ErrDuplicate             Code = "ERR_DUPLICATE"
	ErrLienExceeded          Code = "ERR_LIEN_EXCEEDED"
	ErrAlreadyReleased       Code = "ERR_ALREADY_RELEASED"
	ErrPolicyNotSatisfied    Code = "ERR_POLICY_NOT_SATISFIED"
	ErrQtyMismatch           Code = "ERR_QTY_MISMATCH"
	ErrObjectionWindowClosed Code = "ERR_OBJECTION_WINDOW_CLOSED"
	ErrMseNotApplicable      Code = "ERR_MSE_NOT_APPLICABLE"
	ErrBadSignature          Code = "ERR_BAD_SIGNATURE"
	ErrRateLimited           Code = "ERR_RATE_LIMITED"
	ErrMvccConflict          Code = "ERR_MVCC_CONFLICT"
	ErrInternal              Code = "ERR_INTERNAL"
)

var known = map[Code]bool{
	ErrBadRequest: true, ErrUnauthorized: true, ErrForbiddenRole: true, ErrNotFound: true,
	ErrInvalidTransition: true, ErrDuplicate: true, ErrLienExceeded: true, ErrAlreadyReleased: true,
	ErrPolicyNotSatisfied: true, ErrQtyMismatch: true, ErrObjectionWindowClosed: true,
	ErrMseNotApplicable: true, ErrBadSignature: true, ErrRateLimited: true, ErrMvccConflict: true,
	ErrInternal: true,
}

// Known reports whether c is a frozen error code.
func Known(c Code) bool { return known[c] }

// Error is a typed application error.
//
// Retryable means the operation is known NOT to have taken effect (e.g. endorsement failed, or the
// transaction was invalidated by an MVCC conflict), so it is safe to re-run automatically.
// An ambiguous outcome (submitted but commit status unknown) is never Retryable.
type Error struct {
	Code      Code
	Message   string
	Details   map[string]any
	TxID      string
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	if e.Err != nil && e.Message == "" {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// New returns an Error with a fixed message.
func New(code Code, msg string) *Error { return &Error{Code: code, Message: msg} }

// Newf returns an Error with a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a code to an underlying error.
func Wrap(code Code, err error, msg string) *Error {
	return &Error{Code: code, Message: msg, Err: err}
}

// WithDetail returns e with one extra detail set.
func (e *Error) WithDetail(k string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}

// As extracts an *Error from err.
func As(err error) (*Error, bool) {
	var ae *Error
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// CodeOf returns the code carried by err, or ERR_INTERNAL.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	if ae, ok := As(err); ok {
		return ae.Code
	}
	return ErrInternal
}

// Is reports whether err carries code c.
func Is(err error, c Code) bool { return err != nil && CodeOf(err) == c }

// HTTPStatus maps a code to its HTTP status (BUILD.md §12).
//
// ERR_OBJECTION_WINDOW_CLOSED and ERR_MSE_NOT_APPLICABLE are not listed in the §12 mapping; they
// are business-rule rejections, so they map to 409 and 400 rather than a misleading 5xx.
func HTTPStatus(c Code) int {
	switch c {
	case ErrBadRequest, ErrQtyMismatch, ErrMseNotApplicable:
		return http.StatusBadRequest
	case ErrUnauthorized, ErrBadSignature:
		return http.StatusUnauthorized
	case ErrForbiddenRole:
		return http.StatusForbidden
	case ErrNotFound:
		return http.StatusNotFound
	case ErrInvalidTransition, ErrDuplicate, ErrAlreadyReleased, ErrLienExceeded,
		ErrPolicyNotSatisfied, ErrObjectionWindowClosed:
		return http.StatusConflict
	case ErrRateLimited:
		return http.StatusTooManyRequests
	case ErrMvccConflict:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

var codeRe = regexp.MustCompile(`ERR_[A-Z_]+`)

// ExtractCode finds the first frozen ERR_* code inside free text, such as a chaincode error
// message relayed by the peer ("chaincode response 500, ERR_DUPLICATE: idem key seen").
func ExtractCode(s string) (Code, bool) {
	for _, m := range codeRe.FindAllString(s, -1) {
		if c := Code(m); known[c] {
			return c, true
		}
	}
	return "", false
}
