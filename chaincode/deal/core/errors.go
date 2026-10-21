// Package core holds all Anubandh deal logic. It has NO Fabric imports so it can be
// unit-tested without a peer. contract.go (package main) adapts it to Fabric.
package core

import (
	"errors"
	"fmt"
)

// Code is a frozen error code (BUILD.md §12). Messages always start with the code
// so the gateway can map "ERR_XXX: text" to HTTP status.
type Code string

const (
	ErrBadRequest          Code = "ERR_BAD_REQUEST"
	ErrUnauthorized        Code = "ERR_UNAUTHORIZED"
	ErrForbiddenRole       Code = "ERR_FORBIDDEN_ROLE"
	ErrNotFound            Code = "ERR_NOT_FOUND"
	ErrInvalidTransition   Code = "ERR_INVALID_TRANSITION"
	ErrDuplicate           Code = "ERR_DUPLICATE"
	ErrLienExceeded        Code = "ERR_LIEN_EXCEEDED"
	ErrAlreadyReleased     Code = "ERR_ALREADY_RELEASED"
	ErrPolicyNotSatisfied  Code = "ERR_POLICY_NOT_SATISFIED"
	ErrQtyMismatch         Code = "ERR_QTY_MISMATCH"
	ErrObjectionWindowShut Code = "ERR_OBJECTION_WINDOW_CLOSED"
	ErrMseNotApplicable    Code = "ERR_MSE_NOT_APPLICABLE"
	ErrInternal            Code = "ERR_INTERNAL"
)

// Error is the only error type the engine returns for business failures.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func fail(c Code, format string, a ...any) error {
	return &Error{Code: c, Message: fmt.Sprintf(format, a...)}
}

// CodeOf extracts the code of err (ERR_INTERNAL if it is not an *Error).
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ErrInternal
}
