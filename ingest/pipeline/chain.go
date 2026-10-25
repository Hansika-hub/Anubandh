package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Gateway error codes the worker reacts to (frozen, BUILD.md section 12).
const (
	CodeInvalidTransition = "ERR_INVALID_TRANSITION"
	CodeDuplicate         = "ERR_DUPLICATE"
)

// ChainError is a failed gateway submit. Code is empty for transport errors and non-JSON 5xx.
type ChainError struct {
	Status  int
	Code    string
	Message string
}

func (e *ChainError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("gateway submit failed (status %d): %s", e.Status, e.Message)
	}
	return fmt.Sprintf("gateway submit failed (status %d) %s: %s", e.Status, e.Code, e.Message)
}

// Chain submits one chaincode call. It is the seam where the worker meets the gateway.
type Chain interface {
	Submit(ctx context.Context, fn string, args []string, idemKey string) (txID string, err error)
}

// HTTPChain talks to POST {BaseURL}/internal/v1/submit.
type HTTPChain struct {
	BaseURL string
	Token   string // X-Internal-Token; empty when the gateway has none
	Client  *http.Client
}

func NewHTTPChain(baseURL, token string) *HTTPChain {
	return &HTTPChain{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Client: &http.Client{Timeout: 70 * time.Second}}
}

func (c *HTTPChain) Submit(ctx context.Context, fn string, args []string, idemKey string) (string, error) {
	body, _ := json.Marshal(map[string]any{"fn": fn, "args": args, "idemKey": idemKey})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/v1/submit", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("X-Internal-Token", c.Token)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", &ChainError{Message: err.Error()} // transport failure: retryable
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK {
		var ok struct {
			TxID string `json:"txId"`
		}
		if err := json.Unmarshal(raw, &ok); err != nil {
			return "", &ChainError{Status: resp.StatusCode, Message: "200 with unreadable body"}
		}
		return ok.TxID, nil
	}
	ce := &ChainError{Status: resp.StatusCode}
	var eb struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &eb) == nil && eb.Error.Code != "" {
		ce.Code, ce.Message = eb.Error.Code, eb.Error.Message
	} else {
		ce.Message = strings.TrimSpace(string(raw))
		if len(ce.Message) > 200 {
			ce.Message = ce.Message[:200]
		}
	}
	return "", ce
}

// MockChain is GATEWAY_MOCK=true: it accepts everything with a deterministic tx id (BUILD.md 14.4).
type MockChain struct{}

func (MockChain) Submit(_ context.Context, fn string, _ []string, idemKey string) (string, error) {
	return "mock-" + fn + "-" + idemKey, nil
}
