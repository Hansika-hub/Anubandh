package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestHTTPStatusMatchesBuildSection12(t *testing.T) {
	want := map[Code]int{
		ErrBadRequest: 400, ErrQtyMismatch: 400,
		ErrUnauthorized: 401, ErrBadSignature: 401,
		ErrForbiddenRole:     403,
		ErrNotFound:          404,
		ErrInvalidTransition: 409, ErrDuplicate: 409, ErrAlreadyReleased: 409, ErrLienExceeded: 409, ErrPolicyNotSatisfied: 409,
		ErrRateLimited:  429,
		ErrMvccConflict: 503,
		ErrInternal:     500,
		// Not listed in §12; business-rule rejections, never 5xx.
		ErrObjectionWindowClosed: 409, ErrMseNotApplicable: 400,
	}
	for c := range known {
		if _, ok := want[c]; !ok {
			t.Errorf("code %s has no expected status in this test", c)
		}
	}
	for c, s := range want {
		if got := HTTPStatus(c); got != s {
			t.Errorf("HTTPStatus(%s) = %d, want %d", c, got, s)
		}
	}
	if HTTPStatus("ERR_SOMETHING_ELSE") != http.StatusInternalServerError {
		t.Error("unknown codes must map to 500")
	}
}

func TestExtractCodeFromPeerMessages(t *testing.T) {
	cases := map[string]Code{
		"rpc error: code = Aborted desc = failed to endorse transaction, see attached details for more info | chaincode response 500, ERR_DUPLICATE: idem key warehouse:GR-1 already processed": ErrDuplicate,
		"evaluate call to endorser returned error: chaincode response 500, ERR_NOT_FOUND: deal DL-20261020-0009 not found":                                                                      ErrNotFound,
		"ERR_INVALID_TRANSITION tranche 1 has no goods receipt yet":                                                                                                                             ErrInvalidTransition,
		"ERR_UNKNOWN_THING then ERR_LIEN_EXCEEDED": ErrLienExceeded,
	}
	for msg, want := range cases {
		got, ok := ExtractCode(msg)
		if !ok || got != want {
			t.Errorf("ExtractCode(%q) = %q,%v want %q", msg, got, ok, want)
		}
	}
	if _, ok := ExtractCode("plain failure, no code"); ok {
		t.Error("expected no code")
	}
	if _, ok := ExtractCode("ERR_NOT_A_REAL_CODE"); ok {
		t.Error("unknown ERR_ tokens must not be treated as codes")
	}
}

func TestCodeOfWrapped(t *testing.T) {
	base := New(ErrPolicyNotSatisfied, "needs CFO")
	wrapped := fmt.Errorf("release: %w", base)
	if CodeOf(wrapped) != ErrPolicyNotSatisfied || !Is(wrapped, ErrPolicyNotSatisfied) {
		t.Fatal("code must survive wrapping")
	}
	if CodeOf(errors.New("x")) != ErrInternal {
		t.Fatal("untyped errors are ERR_INTERNAL")
	}
	if CodeOf(nil) != "" {
		t.Fatal("nil has no code")
	}
}
