package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Hansika-hub/anubandh/gateway/internal/apperr"
	"github.com/Hansika-hub/anubandh/gateway/internal/fabric"
	"github.com/Hansika-hub/anubandh/gateway/internal/model"
	"github.com/Hansika-hub/anubandh/gateway/internal/release"
)

func trancheArgs(r *http.Request) (string, int64, error) {
	dealID, err := pathDealID(r)
	if err != nil {
		return "", 0, err
	}
	n, err := pathInt(r, "n")
	return dealID, n, err
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (h *Handlers) ackDelivery(w http.ResponseWriter, r *http.Request) {
	dealID, n, err := trancheArgs(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	h.write(w, r, dealID, []model.Org{model.OrgSupplier},
		fabric.SubmitRequest{Fn: "AcknowledgeDelivery", Args: []string{dealID, itoa(n)}}, map[string]any{"trancheNo": n})
}

// approve records the caller's approval; when the approval policy is then satisfied, it
// releases the tranche immediately (BUILD.md §8.3 "auto after approval").
func (h *Handlers) approve(w http.ResponseWriter, r *http.Request) {
	dealID, n, err := trancheArgs(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	id, err := requireUser(principalFrom(r.Context()), model.OrgBuyer)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	if err := h.checkParty(r.Context(), id, dealID); err != nil {
		writeErr(w, h.log, err)
		return
	}
	ctx, cancel := chainCtx(r, id)
	defer cancel()
	res, err := h.chain.Submit(ctx, fabric.SubmitRequest{Fn: "ApproveRelease", Args: []string{dealID, itoa(n)}})
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	out := map[string]any{"txId": res.TxID, "dealId": dealID, "trancheNo": n, "role": id.Role, "satisfied": false, "release": nil}
	if b, err := h.chain.Evaluate(ctx, "GetTranche", dealID, itoa(n)); err == nil {
		var t model.Tranche
		if json.Unmarshal(b, &t) == nil && (t.Status == model.TrancheApproved || t.Status == model.TrancheReleased || t.Status == model.TrancheSettled) {
			out["satisfied"] = true
			if h.autoRelease && t.Status == model.TrancheApproved {
				key := release.IdemKey(dealID, n)
				rel, err := release.Release(ctx, h.chain, dealID, n, key)
				switch {
				case err == nil:
					out["release"] = map[string]any{"txId": rel.TxID, "idemKey": key}
					h.log.Info("tranche auto-released", "dealId", dealID, "trancheNo", n, "txId", rel.TxID, "idemKey", key)
				case release.AlreadyDone(err):
					out["release"] = map[string]any{"idemKey": key, "alreadyReleased": true}
				default:
					ae, _ := apperr.As(err)
					code := apperr.CodeOf(err)
					msg := err.Error()
					if ae != nil {
						msg = ae.Message
					}
					out["release"] = map[string]any{"idemKey": key, "error": map[string]any{"code": code, "message": msg}}
					h.log.Warn("auto-release failed after approval", "dealId", dealID, "trancheNo", n, "err", err)
				}
			}
		}
	}
	h.reconcile(ctx, dealID)
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) releaseTranche(w http.ResponseWriter, r *http.Request) {
	dealID, n, err := trancheArgs(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	var body struct {
		IdemKey string `json:"idemKey"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, h.log, err)
		return
	}
	key := strings.TrimSpace(body.IdemKey)
	if key == "" {
		key = strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
	}
	if key == "" {
		key = release.IdemKey(dealID, n) // deterministic: a double click can never release twice
	}
	if !model.IdemKeyRe.MatchString(key) {
		writeErr(w, h.log, errBadRequest(idemKeyMsg))
		return
	}
	h.write(w, r, dealID, []model.Org{model.OrgBuyer},
		fabric.SubmitRequest{Fn: "ReleaseTranche", Args: []string{dealID, itoa(n), key}}, map[string]any{"trancheNo": n, "idemKey": key})
}

func (h *Handlers) raiseObjection(w http.ResponseWriter, r *http.Request) {
	dealID, n, err := trancheArgs(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	reason, err := reasonBody(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	h.write(w, r, dealID, []model.Org{model.OrgBuyer},
		fabric.SubmitRequest{Fn: "RaiseObjection", Args: []string{dealID, itoa(n), reason}}, map[string]any{"trancheNo": n})
}

func (h *Handlers) resolveObjection(w http.ResponseWriter, r *http.Request) {
	dealID, n, err := trancheArgs(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	h.write(w, r, dealID, []model.Org{model.OrgBuyer, model.OrgSupplier},
		fabric.SubmitRequest{Fn: "ResolveObjection", Args: []string{dealID, itoa(n)}}, map[string]any{"trancheNo": n})
}

func reasonBody(r *http.Request) (string, error) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &body); err != nil {
		return "", err
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" || len(reason) > 1000 {
		return "", errBadRequest("reason is required (max 1000 characters)")
	}
	return reason, nil
}

// ---- held portions ----

func heldPath(r *http.Request) (string, int64, int64, error) {
	dealID, n, err := trancheArgs(r)
	if err != nil {
		return "", 0, 0, err
	}
	ln, err := pathInt(r, "lineNo")
	return dealID, n, ln, err
}

func positiveBody(r *http.Request, field string) (int64, error) {
	var body map[string]json.RawMessage
	if err := decode(r, &body); err != nil {
		return 0, apperr.Newf(apperr.ErrBadRequest, "body must be {\"%s\": <positive integer>}", field)
	}
	v, err := strconv.ParseInt(string(body[field]), 10, 64)
	if err != nil || v <= 0 {
		return 0, apperr.Newf(apperr.ErrBadRequest, "%s must be a positive integer", field)
	}
	return v, nil
}

func (h *Handlers) heldWrite(w http.ResponseWriter, r *http.Request, fn string, orgs []model.Org, field string) {
	dealID, n, ln, err := heldPath(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	args := []string{dealID, itoa(n), itoa(ln)}
	extra := map[string]any{"trancheNo": n, "lineNo": ln}
	if field != "" {
		v, err := positiveBody(r, field)
		if err != nil {
			writeErr(w, h.log, err)
			return
		}
		args = append(args, itoa(v))
		extra[field] = v
	}
	h.write(w, r, dealID, orgs, fabric.SubmitRequest{Fn: fn, Args: args}, extra)
}

var parties = []model.Org{model.OrgBuyer, model.OrgSupplier}

func (h *Handlers) shipReplacement(w http.ResponseWriter, r *http.Request) {
	h.heldWrite(w, r, "ShipReplacement", []model.Org{model.OrgSupplier}, "qty")
}

func (h *Handlers) proposeCreditNote(w http.ResponseWriter, r *http.Request) {
	h.heldWrite(w, r, "ProposeCreditNote", parties, "valuePaise")
}

func (h *Handlers) signCreditNote(w http.ResponseWriter, r *http.Request) {
	h.heldWrite(w, r, "SignCreditNote", parties, "")
}

func (h *Handlers) proposePriceAdjustment(w http.ResponseWriter, r *http.Request) {
	h.heldWrite(w, r, "ProposePriceAdjustment", parties, "adjustedUnitPricePaise")
}

func (h *Handlers) signPriceAdjustment(w http.ResponseWriter, r *http.Request) {
	h.heldWrite(w, r, "SignPriceAdjustment", parties, "")
}

func (h *Handlers) raiseDispute(w http.ResponseWriter, r *http.Request) {
	dealID, n, ln, err := heldPath(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	reason, err := reasonBody(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	h.write(w, r, dealID, parties, fabric.SubmitRequest{Fn: "RaiseDispute", Args: []string{dealID, itoa(n), itoa(ln), reason}},
		map[string]any{"trancheNo": n, "lineNo": ln})
}

func (h *Handlers) resolveDispute(w http.ResponseWriter, r *http.Request) {
	dealID, n, ln, err := heldPath(r)
	if err != nil {
		writeErr(w, h.log, err)
		return
	}
	if p := principalFrom(r.Context()); p.User != nil && p.User.Role != model.RoleArbiter {
		writeErr(w, h.log, apperr.New(apperr.ErrForbiddenRole, "dispute resolution requires role ARBITER"))
		return
	}
	var body struct {
		Resolution string `json:"resolution"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, h.log, err)
		return
	}
	if body.Resolution != model.ResolutionReleaseToSupplier && body.Resolution != model.ResolutionRefundToBuyer {
		writeErr(w, h.log, errBadRequest("resolution must be RELEASE_TO_SUPPLIER or REFUND_TO_BUYER"))
		return
	}
	h.write(w, r, dealID, []model.Org{model.OrgBank}, fabric.SubmitRequest{Fn: "ResolveDispute", Args: []string{dealID, itoa(n), itoa(ln), body.Resolution}},
		map[string]any{"trancheNo": n, "lineNo": ln, "resolution": body.Resolution})
}
