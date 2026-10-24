package core

import "regexp"

var idemRe = regexp.MustCompile(`^[a-z0-9_-]+:[A-Za-z0-9._-]+$`)

// consumeIdem records an idempotency key ("<source>:<sourceEventId>"). A repeated key fails
// with ERR_DUPLICATE. Because the key is READ then WRITTEN, two concurrent transactions with
// the same key conflict at commit (MVCC) and only one wins — no double effect either way.
func (e *Engine) consumeIdem(key string) error {
	if !idemRe.MatchString(key) {
		return fail(ErrBadRequest, "idemKey must look like <source>:<id>, got %q", key)
	}
	b, err := e.get(idemStoreKey(key))
	if err != nil {
		return fail(ErrInternal, "idem read: %v", err)
	}
	if b != nil {
		return fail(ErrDuplicate, "idempotency key %s already processed", key)
	}
	if err := e.put(idemStoreKey(key), []byte("1")); err != nil {
		return fail(ErrInternal, "idem write: %v", err)
	}
	return nil
}
