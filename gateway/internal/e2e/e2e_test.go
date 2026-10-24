package e2e

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	demoID = "DL-20261020-0001" // ₹10L, SUP-007 SMALL, 45-day agreement (BUILD.md §11 fixtures)
	bigID  = "DL-20261020-0002" // ₹60L, SUP-012 MICRO, 15-day: needs PLANT_HEAD + CFO
)

func (e *env) createDemo() {
	e.t.Helper()
	r := e.must(201, e.do("POST", "/api/v1/deals", "BUYER", "buyer-procurement", e.dealBody(demoID, "SUP-007", "SMALL", 45, true, 100, 1000000, "")), "create demo deal")
	if num(r.body["lienAmountPaise"]) != 100_000_000 {
		e.t.Fatalf("lien defaults to PO value: %s", r.raw)
	}
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/lien", "BANK", "bank-officer", map[string]any{"lienRef": "LIEN-9921", "amountPaise": 100_000_000}), "lien")
}

// TestDemoScenario is BUILD.md §15 "Demo scenario" steps 1-6, end to end.
func TestDemoScenario(t *testing.T) {
	e := setup(t)
	e.createDemo()

	// 3. Warehouse GRN for 100 units, sent twice and shuffled with the QC event.
	e.mustErr(409, "ERR_INVALID_TRANSITION", e.internal("RecordQualityCheck", demoID, "1", `[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]`, "warehouse:QC-501"), "QC before GRN")
	e.must(200, e.internal("PostGoodsReceipt", demoID, "1", `[{"lineNo":1,"deliveredQty":100}]`, "warehouse:GR-88123"), "GRN")
	e.mustErr(409, "ERR_DUPLICATE", e.internal("PostGoodsReceipt", demoID, "1", `[{"lineNo":1,"deliveredQty":100}]`, "warehouse:GR-88123"), "duplicate GRN")
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/ack", "SUPPLIER", "supplier-007", nil), "supplier ack")

	// 4. QC: 80 accepted / 20 rejected (the requeued out-of-order event now succeeds).
	e.must(200, e.internal("RecordQualityCheck", demoID, "1", `[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]`, "warehouse:QC-501"), "QC")
	e.waitIdle()
	d := e.must(200, e.do("GET", "/api/v1/deals/"+demoID, "BUYER", "buyer-procurement", nil), "detail").body
	if d["status"] != "PARTIALLY_ACCEPTED" || num(d["totalHeldPaise"]) != 20_000_000 {
		t.Fatalf("after QC: %v held %v", d["status"], d["totalHeldPaise"])
	}
	tr := obj(arr(d["tranches"])[0])
	if tr["status"] != "ACCEPTED" || num(tr["acceptedValuePaise"]) != 80_000_000 || tr["mseDeadline"] == nil {
		t.Fatalf("tranche after QC: %v", tr)
	}
	start, _ := time.Parse(time.RFC3339, tr["mseClockStart"].(string))
	deadline, _ := time.Parse(time.RFC3339, tr["mseDeadline"].(string))
	if deadline.Sub(start) != 45*24*time.Hour || tr["mseClockStart"] != tr["deliveredAt"] {
		t.Fatalf("45-day MSE clock must run from the delivery day: %v -> %v (delivered %v)", start, deadline, tr["deliveredAt"])
	}
	line := obj(arr(d["lines"])[0])
	if num(line["deliveredQty"]) != 100 || num(line["acceptedQty"]) != 80 || num(line["rejectedQty"]) != 20 || num(line["heldQty"]) != 20 || num(line["unitPricePaise"]) != 1000000 {
		t.Fatalf("line aggregates: %v", line)
	}
	if held := arr(d["heldPortions"]); len(held) != 1 || num(obj(held[0])["qty"]) != 20 {
		t.Fatalf("held portions: %v", held)
	}
	if lines := arr(tr["lines"]); len(lines) != 1 || num(obj(lines[0])["rejectedQty"]) != 20 {
		t.Fatalf("tranche lines enriched from ledger: %v", tr["lines"])
	}

	// 5. ₹8L: PLANT_HEAD approval satisfies the policy -> auto release over NEFT; ₹2L held.
	ap := e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/approve", "BUYER", "buyer-planthead", nil), "approve").body
	if ap["satisfied"] != true || obj(ap["release"])["txId"] == nil {
		t.Fatalf("approval must auto-release: %v", ap)
	}
	e.waitIdle()
	e.eventually("outbox row", func() bool { return e.scalar(`SELECT count(*) FROM payment_outbox`) == 1 })
	if rail := e.text(`SELECT rail FROM payment_outbox WHERE deal_id=$1`, demoID); rail != "NEFT" {
		t.Fatalf("₹8L must go over NEFT, got %s", rail)
	}
	e.processOutbox()
	e.waitIdle()
	ob := e.must(200, e.do("GET", "/api/v1/payments/outbox", "BANK", "", nil), "outbox").body
	row := obj(arr(ob["items"])[0])
	if row["status"] != "CONFIRMED" || num(row["amountPaise"]) != 80_000_000 || row["bankRef"] != "UTR-0001" {
		t.Fatalf("outbox: %v", row)
	}
	d = e.must(200, e.do("GET", "/api/v1/deals/"+demoID, "BUYER", "", nil), "detail").body
	tr = obj(arr(d["tranches"])[0])
	if tr["status"] != "SETTLED" || tr["bankRef"] != "UTR-0001" || tr["rail"] != "NEFT" || tr["settledAt"] == nil || tr["releasedAt"] == nil {
		t.Fatalf("tranche after settlement: %v", tr)
	}
	if num(d["totalReleasedPaise"]) != 80_000_000 {
		t.Fatalf("exactly 80%% released: %v", d["totalReleasedPaise"])
	}

	// 6. Supplier ships 20 replacements -> new tranche, delivered, accepted, released: PO complete.
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/held/1/1/replacement", "SUPPLIER", "supplier-007", map[string]any{"qty": 20}), "replacement")
	e.must(200, e.internal("PostGoodsReceipt", demoID, "2", `[{"lineNo":1,"deliveredQty":20}]`, "warehouse:GR-88124"), "GRN 2")
	e.must(200, e.internal("RecordQualityCheck", demoID, "2", `[{"lineNo":1,"acceptedQty":20,"rejectedQty":0}]`, "warehouse:QC-502"), "QC 2")
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/2/approve", "BUYER", "buyer-planthead", nil), "approve 2")
	e.waitIdle()
	e.eventually("2 outbox rows", func() bool { return e.scalar(`SELECT count(*) FROM payment_outbox`) == 2 })
	e.processOutbox()
	e.waitIdle()
	d = e.must(200, e.do("GET", "/api/v1/deals/"+demoID, "BUYER", "", nil), "detail").body
	if d["status"] != "SETTLED" || num(d["totalReleasedPaise"]) != 100_000_000 || num(d["totalHeldPaise"]) != 0 {
		t.Fatalf("final deal: status %v released %v held %v", d["status"], d["totalReleasedPaise"], d["totalHeldPaise"])
	}
	if p := e.bank.payments(); len(p) != 2 {
		t.Fatalf("bank must receive exactly 2 instructions, got %d", len(p))
	}
	hist := e.must(200, e.do("GET", "/api/v1/deals/"+demoID+"/history", "AUDITOR", "auditor", nil), "history").body
	if num(hist["total"]) < 8 {
		t.Fatalf("ledger history: %v", hist["total"])
	}
	// Auditor sees the deal but never line prices.
	ad := e.must(200, e.do("GET", "/api/v1/deals/"+demoID, "AUDITOR", "auditor", nil), "auditor detail")
	if strings.Contains(string(ad.raw), "unitPricePaise") {
		t.Fatalf("auditor must not see unit prices: %s", ad.raw)
	}
}

// TestBurstNoDoubleRelease hammers one approved tranche with concurrent releases and replays the
// whole event stream twice: one release, one outbox row, one bank instruction.
func TestBurstNoDoubleRelease(t *testing.T) {
	e := setupWith(t, options{noAutoRelease: true})
	e.createDemo()
	// Duplicates of every inbound event, out of order.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.internal("PostGoodsReceipt", demoID, "1", `[{"lineNo":1,"deliveredQty":100}]`, "warehouse:GR-1")
			e.internal("RecordQualityCheck", demoID, "1", `[{"lineNo":1,"acceptedQty":100,"rejectedQty":0}]`, "warehouse:QC-1")
		}()
	}
	wg.Wait()
	e.internal("RecordQualityCheck", demoID, "1", `[{"lineNo":1,"acceptedQty":100,"rejectedQty":0}]`, "warehouse:QC-1")
	e.waitIdle()
	if n := e.scalar(`SELECT count(*) FROM deal_events WHERE event_name IN ('GoodsReceived','QualityRecorded')`); n != 2 {
		t.Fatalf("exactly one GRN and one QC may commit, got %d", n)
	}
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/approve", "BUYER", "buyer-planthead", nil), "approve")

	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := map[string]any{}
			if i%2 == 0 {
				body["idemKey"] = fmt.Sprintf("web:click-%d", i)
			}
			r := e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/release", "BUYER", "buyer-cfo", body)
			mu.Lock()
			codes[r.status]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if codes[200] != 1 || codes[200]+codes[409] != 40 {
		t.Fatalf("want exactly one 200 and the rest 409, got %v", codes)
	}
	e.waitIdle()
	e.eventually("outbox row", func() bool { return e.scalar(`SELECT count(*) FROM payment_outbox`) == 1 })
	var pw sync.WaitGroup
	for i := 0; i < 4; i++ { // concurrent outbox workers (several gateway replicas)
		pw.Add(1)
		go func() { defer pw.Done(); _ = e.app.Adapter.ProcessOnce(e.ctx) }()
	}
	pw.Wait()
	e.processOutbox()

	// Full replay of the channel from block 0 must not duplicate anything.
	events := e.scalar(`SELECT count(*) FROM deal_events`)
	e.stopListener()
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM chain_checkpoint`); err != nil {
		t.Fatal(err)
	}
	e.startListener()
	e.eventually("replay done", func() bool {
		s := e.app.Listener.Status()
		return s.Processed > 0 && s.BlockNo >= e.mock.Height()-1
	})
	time.Sleep(200 * time.Millisecond)
	e.processOutbox()
	if n := e.scalar(`SELECT count(*) FROM deal_events`); n != events {
		t.Fatalf("replay duplicated events: %d -> %d", events, n)
	}
	if n := e.scalar(`SELECT count(*) FROM payment_outbox`); n != 1 {
		t.Fatalf("replay duplicated payments: %d", n)
	}
	p := e.bank.payments()
	if len(p) != 1 {
		t.Fatalf("bank instructions: %d", len(p))
	}
	for _, v := range p {
		if v.instr.AmountPaise != 100_000_000 {
			t.Fatalf("amount %d", v.instr.AmountPaise)
		}
	}
	if s := e.text(`SELECT status FROM payment_outbox`); s != "CONFIRMED" {
		t.Fatalf("outbox status %s", s)
	}
	if n := e.scalar(`SELECT total_released_paise FROM deals WHERE deal_id=$1`, demoID); n != 100_000_000 {
		t.Fatalf("released %d", n)
	}
}

// TestDeemedAcceptanceAndBigDealPolicy: BUILD.md demo step 7 + the ₹60L two-approver deal.
func TestDeemedAcceptanceAndBigDealPolicy(t *testing.T) {
	e := setup(t)
	e.must(201, e.do("POST", "/api/v1/deals", "BUYER", "buyer-procurement", e.dealBody(bigID, "SUP-012", "MICRO", 15, true, 60, 10000000, "")), "create big deal")
	e.must(200, e.do("POST", "/api/v1/deals/"+bigID+"/lien", "BANK", "bank-officer", map[string]any{"lienRef": "LIEN-2", "amountPaise": 600_000_000}), "lien")
	e.must(200, e.internal("PostGoodsReceipt", bigID, "1", `[{"lineNo":1,"deliveredQty":60}]`, "warehouse:GR-2001"), "GRN")
	e.waitIdle()

	// One day past the 15-day deemed-acceptance window: the MICRO supplier's 15-day payment term,
	// which runs from the delivery day (MSMED Act §2(b)(ii)), is already one day overdue.
	r := e.must(200, e.do("POST", "/internal/v1/mock/clock", "", "", map[string]any{"advanceDays": 16}), "fast-forward")
	if num(r.body["dealsTicked"]) < 1 {
		t.Fatalf("clock advance must tick deadlines: %s", r.raw)
	}
	e.waitIdle()
	d := e.must(200, e.do("GET", "/api/v1/deals/"+bigID, "BUYER", "", nil), "detail").body
	tr := obj(arr(d["tranches"])[0])
	if tr["status"] != "DEEMED_ACCEPTED" || tr["deemedAcceptance"] != true || num(tr["acceptedValuePaise"]) != 600_000_000 {
		t.Fatalf("deemed acceptance: %v", tr)
	}
	clocks := e.must(200, e.do("GET", "/api/v1/mse/clocks?riskDays=5", "BUYER", "", nil), "clocks").body
	c := obj(arr(clocks["items"])[0])
	if c["dealId"] != bigID || c["atRisk"] != true || c["overdue"] != true || c["paid"] != false || num(c["daysLeft"]) != -1 {
		t.Fatalf("15-day MICRO clock from the delivery day is due now: %v", c)
	}

	// ₹60L needs PLANT_HEAD + CFO.
	ap := e.must(200, e.do("POST", "/api/v1/deals/"+bigID+"/tranches/1/approve", "BUYER", "buyer-planthead", nil), "plant head").body
	if ap["satisfied"] != false || ap["release"] != nil {
		t.Fatalf("one approver is not enough for ₹60L: %v", ap)
	}
	e.mustErr(409, "ERR_POLICY_NOT_SATISFIED", e.do("POST", "/api/v1/deals/"+bigID+"/tranches/1/release", "BUYER", "buyer-planthead", nil), "early release")
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.do("POST", "/api/v1/deals/"+bigID+"/tranches/1/approve", "BUYER", "buyer-warehouse", nil), "non-approver role")
	ap = e.must(200, e.do("POST", "/api/v1/deals/"+bigID+"/tranches/1/approve", "BUYER", "buyer-cfo", nil), "cfo").body
	if ap["satisfied"] != true || obj(ap["release"])["txId"] == nil {
		t.Fatalf("PLANT_HEAD + CFO must release: %v", ap)
	}
	e.waitIdle()
	e.eventually("outbox", func() bool { return e.scalar(`SELECT count(*) FROM payment_outbox`) == 1 })
	e.processOutbox()
	e.waitIdle()

	rep := e.must(200, e.do("GET", "/api/v1/mse/compliance-report", "AUDITOR", "auditor", nil), "report").body
	item := obj(arr(rep["items"])[0])
	if item["complianceStatus"] != "PAID_LATE" || item["onTime"] != false || item["deemedAcceptance"] != true || num(item["daysLate"]) < 1 || item["acceptanceDate"] != item["deliveredAt"] {
		t.Fatalf("compliance row: %v", item)
	}
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.do("GET", "/api/v1/mse/compliance-report", "BUYER", "", nil), "report is auditor-only")
	csvResp := e.do("GET", "/api/v1/mse/compliance-report?format=csv", "AUDITOR", "", nil)
	if csvResp.status != 200 || !strings.HasPrefix(csvResp.header.Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %s", csvResp.status, csvResp.header.Get("Content-Type"))
	}
	recs, err := csv.NewReader(strings.NewReader(string(csvResp.raw))).ReadAll()
	if err != nil || len(recs) != 2 || recs[0][0] != "dealId" || recs[1][0] != bigID || recs[1][15] != "PAID_LATE" {
		t.Fatalf("csv rows: %v %v", recs, err)
	}
}

// deadlineRe matches the deliveryDeadline field whatever its value (it is derived from the clock).
var deadlineRe = regexp.MustCompile(`"deliveryDeadline":"[^"]*"`)

func TestAuthAndValidation(t *testing.T) {
	e := setup(t)
	body := e.dealBody(demoID, "SUP-007", "SMALL", 45, true, 100, 1000000, "")
	e.mustErr(401, "ERR_UNAUTHORIZED", e.do("GET", "/api/v1/deals", "", "", nil), "no X-Org")
	e.mustErr(401, "ERR_UNAUTHORIZED", e.do("GET", "/api/v1/deals", "BOSS", "", nil), "bad org")
	e.mustErr(401, "ERR_UNAUTHORIZED", e.do("POST", "/api/v1/deals", "BUYER", "mallory", body), "unknown user")
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.do("POST", "/api/v1/deals", "BUYER", "supplier-007", body), "user/org mismatch")
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.do("POST", "/api/v1/deals", "SUPPLIER", "supplier-007", body), "supplier creates deal")
	e.mustErr(401, "ERR_UNAUTHORIZED", e.do("POST", "/api/v1/deals", "BUYER", "", body), "write without X-User")

	bad := map[string]string{
		"bad dealId":       strings.Replace(body, demoID, "DL-1", 1),
		"bad date in id":   strings.Replace(body, demoID, "DL-20261399-0001", 1),
		"zero qty":         strings.Replace(body, `"orderedQty":100`, `"orderedQty":0`, 1),
		"float price":      strings.Replace(body, `"unitPricePaise":1000000`, `"unitPricePaise":10.5`, 1),
		"overflow":         strings.Replace(body, `"unitPricePaise":1000000`, `"unitPricePaise":9223372036854775807`, 1),
		"term 30":          strings.Replace(body, `"termDays":45`, `"termDays":30`, 1),
		"bad udyam":        strings.Replace(body, `UDYAM-KL-00-0000007`, `12345`, 1),
		"not json":         `{"dealId":`,
		"past deadline":    deadlineRe.ReplaceAllString(body, `"deliveryDeadline":"2020-01-01T00:00:00Z"`),
		"no zero band":     e.dealBody(demoID, "SUP-007", "SMALL", 45, true, 100, 1000000, `,"approvalPolicy":[{"minPaise":5,"requiredRoles":["CFO"]}]`),
		"bad approver":     e.dealBody(demoID, "SUP-007", "SMALL", 45, true, 100, 1000000, `,"approvalPolicy":[{"minPaise":0,"requiredRoles":["AUDITOR"]}]`),
		"lien below value": e.dealBody(demoID, "SUP-007", "SMALL", 45, true, 100, 1000000, `,"lienAmountPaise":5`),
		"other buyer":      strings.Replace(body, `"buyerId":"BUYER-001"`, `"buyerId":"BUYER-999"`, 1),
	}
	for name, b := range bad {
		r := e.do("POST", "/api/v1/deals", "BUYER", "buyer-procurement", b)
		if r.status != 400 && (name != "other buyer" || r.status != 403) {
			t.Errorf("%s: want 400, got %d %s", name, r.status, r.raw)
		}
	}
	e.mustErr(400, "ERR_MSE_NOT_APPLICABLE", e.do("POST", "/api/v1/deals", "BUYER", "buyer-procurement",
		e.dealBody(demoID, "SUP-007", "MEDIUM", 45, true, 100, 1000000, "")), "MEDIUM is outside MSMED §15")

	e.createDemo()
	e.mustErr(409, "ERR_DUPLICATE", e.do("POST", "/api/v1/deals", "BUYER", "buyer-procurement", body), "duplicate deal")
	e.mustErr(409, "ERR_INVALID_TRANSITION", e.do("POST", "/api/v1/deals/"+demoID+"/lien", "BANK", "bank-officer", map[string]any{"lienRef": "L", "amountPaise": 100_000_000}), "second lien")
	e.mustErr(404, "ERR_NOT_FOUND", e.do("GET", "/api/v1/deals/DL-20261020-0999", "BUYER", "", nil), "missing deal")
	e.mustErr(400, "ERR_BAD_REQUEST", e.do("GET", "/api/v1/deals/hello", "BUYER", "", nil), "malformed deal id")
	e.mustErr(400, "ERR_BAD_REQUEST", e.do("GET", "/api/v1/deals?limit=0", "BUYER", "", nil), "bad limit")
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.do("GET", "/api/v1/payments/outbox", "SUPPLIER", "", nil), "outbox is bank/buyer only")
	e.must(200, e.internal("PostGoodsReceipt", demoID, "1", `[{"lineNo":1,"deliveredQty":10}]`, "warehouse:GR-1"), "GRN")
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/ack", "SUPPLIER", "supplier-012", nil), "another supplier's deal")
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.internal("ReleaseTranche", demoID, "1", "gateway:x"), "internal fn allowlist")
	e.mustErr(400, "ERR_BAD_REQUEST", e.do("POST", "/internal/v1/submit", "", "", map[string]any{"fn": "PostGoodsReceipt", "args": []string{demoID, "2", "[]", "warehouse:A"}, "idemKey": "warehouse:B"}), "idemKey mismatch")
	e.mustErr(400, "ERR_BAD_REQUEST", e.do("POST", "/api/v1/deals/"+demoID+"/held/1/1/replacement", "SUPPLIER", "supplier-007", map[string]any{"qty": -1}), "negative qty")
	e.mustErr(400, "ERR_BAD_REQUEST", e.do("POST", "/api/v1/deals/"+demoID+"/held/1/1/dispute/resolve", "BANK", "bank-arbiter", map[string]any{"resolution": "SPLIT"}), "bad resolution")
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.do("POST", "/api/v1/deals/"+demoID+"/held/1/1/dispute/resolve", "BANK", "bank-officer", map[string]any{"resolution": "REFUND_TO_BUYER"}), "only ARBITER")

	e.waitIdle()
	list := e.must(200, e.do("GET", "/api/v1/deals?status=FUNDED&mse=true&supplierId=SUP-007", "SUPPLIER", "", nil), "list").body
	if num(list["total"]) != 0 { // the deal is DELIVERED now
		t.Fatalf("filter: %v", list)
	}
	list = e.must(200, e.do("GET", "/api/v1/deals?mse=true&supplierId=SUP-007", "SUPPLIER", "", nil), "list").body
	if num(list["total"]) != 1 || obj(arr(list["items"])[0])["dealId"] != demoID {
		t.Fatalf("list: %v", list)
	}
	// CORS preflight for the dashboard origin.
	req, _ := http.NewRequestWithContext(e.ctx, http.MethodOptions, e.srv.URL+"/api/v1/deals", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 204 || res.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("CORS preflight: %d %v", res.StatusCode, res.Header)
	}
	h := e.must(200, e.do("GET", "/health", "", "", nil), "health").body
	if h["status"] != "ok" || h["db"] != "ok" || obj(h["chain"])["mode"] != "mock" {
		t.Fatalf("health: %v", h)
	}
}

func TestHeldPortionAndObjectionFlows(t *testing.T) {
	e := setup(t)
	e.createDemo()
	e.must(200, e.internal("PostGoodsReceipt", demoID, "1", `[{"lineNo":1,"deliveredQty":50}]`, "warehouse:GR-1"), "GRN")
	e.must(200, e.do("POST", "/internal/v1/mock/clock", "", "", map[string]any{"advanceDays": 2}), "2 days later")
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/objection", "BUYER", "buyer-procurement", map[string]any{"reason": "dimension variance"}), "objection")
	e.must(200, e.do("POST", "/internal/v1/mock/clock", "", "", map[string]any{"advanceDays": 3}), "3 days later")
	e.mustErr(409, "ERR_DUPLICATE", e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/objection", "BUYER", "buyer-procurement", map[string]any{"reason": "again"}), "second objection")
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/objection/resolve", "BUYER", "buyer-procurement", nil), "buyer co-sign")
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/objection/resolve", "SUPPLIER", "supplier-007", nil), "supplier co-sign")
	e.must(200, e.internal("RecordQualityCheck", demoID, "1", `[{"lineNo":1,"acceptedQty":40,"rejectedQty":10}]`, "warehouse:QC-1"), "QC")
	e.waitIdle()
	d := e.must(200, e.do("GET", "/api/v1/deals/"+demoID, "BUYER", "", nil), "detail").body
	tr := obj(arr(d["tranches"])[0])
	if tr["mseClockStart"] == tr["deliveredAt"] || obj(tr["objection"])["open"] != false {
		t.Fatalf("resolved objection must move the acceptance day off the delivery day: %v", tr)
	}
	// Credit note: proposed by supplier, signed by buyer.
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/held/1/1/credit-note", "SUPPLIER", "supplier-007", map[string]any{"valuePaise": 10_000_000}), "propose CN")
	e.mustErr(403, "ERR_FORBIDDEN_ROLE", e.do("POST", "/api/v1/deals/"+demoID+"/held/1/1/credit-note/sign", "SUPPLIER", "supplier-007", nil), "self-sign")
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/held/1/1/credit-note/sign", "BUYER", "buyer-procurement", nil), "sign CN")
	e.waitIdle()
	d = e.must(200, e.do("GET", "/api/v1/deals/"+demoID, "BUYER", "", nil), "detail").body
	h := obj(arr(d["heldPortions"])[0])
	if h["exit"] != "CREDIT_NOTE" || h["state"] != "CLOSED" || num(d["totalHeldPaise"]) != 0 {
		t.Fatalf("credit note exit: %v held %v", h, d["totalHeldPaise"])
	}
	// Second delivery -> dispute -> arbiter releases to supplier -> new accepted tranche.
	e.must(200, e.internal("PostGoodsReceipt", demoID, "2", `[{"lineNo":1,"deliveredQty":50}]`, "warehouse:GR-2"), "GRN 2")
	e.must(200, e.internal("RecordQualityCheck", demoID, "2", `[{"lineNo":1,"acceptedQty":45,"rejectedQty":5}]`, "warehouse:QC-2"), "QC 2")
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/held/2/1/dispute", "SUPPLIER", "supplier-007", map[string]any{"reason": "rejection not per spec"}), "dispute")
	if s := e.must(200, e.do("GET", "/api/v1/deals/"+demoID, "BUYER", "", nil), "detail").body["status"]; s != "DISPUTED" {
		t.Fatalf("deal must be DISPUTED, got %v", s)
	}
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/held/2/1/dispute/resolve", "BANK", "bank-arbiter", map[string]any{"resolution": "RELEASE_TO_SUPPLIER"}), "resolve")
	e.waitIdle()
	d = e.must(200, e.do("GET", "/api/v1/deals/"+demoID, "BUYER", "", nil), "detail").body
	trs := arr(d["tranches"])
	if len(trs) != 3 || obj(trs[2])["status"] != "ACCEPTED" || num(obj(trs[2])["acceptedValuePaise"]) != 5_000_000 {
		t.Fatalf("tranche from resolved dispute: %v", trs)
	}
	line := obj(arr(d["lines"])[0])
	if num(line["deliveredQty"]) != 100 || num(line["acceptedQty"]) != 90 || num(line["rejectedQty"]) != 10 || num(line["heldQty"]) != 0 {
		t.Fatalf("line aggregates after exits: %v", line)
	}
}

func TestBankChaosRetriesThenConfirms(t *testing.T) {
	e := setup(t)
	e.createDemo()
	e.must(200, e.internal("PostGoodsReceipt", demoID, "1", `[{"lineNo":1,"deliveredQty":5}]`, "warehouse:GR-1"), "GRN")
	e.must(200, e.internal("RecordQualityCheck", demoID, "1", `[{"lineNo":1,"acceptedQty":5,"rejectedQty":0}]`, "warehouse:QC-1"), "QC")
	e.bank.mu.Lock()
	e.bank.failNext = 2
	e.bank.mu.Unlock()
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/1/approve", "BUYER", "buyer-planthead", nil), "approve")
	e.waitIdle()
	e.eventually("outbox row", func() bool { return e.scalar(`SELECT count(*) FROM payment_outbox`) == 1 })
	if rail := e.text(`SELECT rail FROM payment_outbox`); rail != "UPI" {
		t.Fatalf("₹50k must go over UPI, got %s", rail)
	}
	_ = e.app.Adapter.ProcessOnce(e.ctx)
	if a := e.scalar(`SELECT attempts FROM payment_outbox`); a != 1 || e.text(`SELECT status FROM payment_outbox`) != "PENDING" {
		t.Fatalf("first failure: attempts %d status %s", a, e.text(`SELECT status FROM payment_outbox`))
	}
	// Backoff: the row is not retried before 2^attempts seconds.
	_ = e.app.Adapter.ProcessOnce(e.ctx)
	if a := e.scalar(`SELECT attempts FROM payment_outbox`); a != 1 {
		t.Fatalf("retried before backoff elapsed: attempts %d", a)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE payment_outbox SET updated_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	_ = e.app.Adapter.ProcessOnce(e.ctx) // second chaos failure
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE payment_outbox SET updated_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	e.processOutbox()
	if s := e.text(`SELECT status FROM payment_outbox`); s != "CONFIRMED" {
		t.Fatalf("after chaos: %s (%s)", s, e.text(`SELECT last_error FROM payment_outbox`))
	}
	if p := e.bank.payments(); len(p) != 1 {
		t.Fatalf("one instruction despite retries, got %d", len(p))
	}

	// A permanent bank rejection (4xx) fails the row immediately for operator action.
	e.must(200, e.internal("PostGoodsReceipt", demoID, "2", `[{"lineNo":1,"deliveredQty":5}]`, "warehouse:GR-2"), "GRN 2")
	e.must(200, e.internal("RecordQualityCheck", demoID, "2", `[{"lineNo":1,"acceptedQty":5,"rejectedQty":0}]`, "warehouse:QC-2"), "QC 2")
	e.bank.mu.Lock()
	e.bank.failNext, e.bank.failStatus = 1, 422
	e.bank.mu.Unlock()
	e.must(200, e.do("POST", "/api/v1/deals/"+demoID+"/tranches/2/approve", "BUYER", "buyer-planthead", nil), "approve 2")
	e.waitIdle()
	e.eventually("outbox row 2", func() bool { return e.scalar(`SELECT count(*) FROM payment_outbox`) == 2 })
	_ = e.app.Adapter.ProcessOnce(e.ctx)
	if s := e.text(`SELECT status FROM payment_outbox WHERE tranche_no = 2`); s != "FAILED" {
		t.Fatalf("4xx must be FAILED, got %s", s)
	}
	ob := e.must(200, e.do("GET", "/api/v1/payments/outbox?status=FAILED", "BANK", "", nil), "outbox").body
	if num(ob["total"]) != 1 {
		t.Fatalf("FAILED filter: %v", ob)
	}
}

func TestListenerResumesFromCheckpoint(t *testing.T) {
	e := setup(t)
	e.createDemo()
	e.waitIdle()
	e.stopListener()
	// Chain moves on while the gateway's listener is down.
	e.must(200, e.internal("PostGoodsReceipt", demoID, "1", `[{"lineNo":1,"deliveredQty":10}]`, "warehouse:GR-1"), "GRN")
	e.must(200, e.internal("RecordQualityCheck", demoID, "1", `[{"lineNo":1,"acceptedQty":10,"rejectedQty":0}]`, "warehouse:QC-1"), "QC")
	before := e.scalar(`SELECT count(*) FROM deal_events`)
	e.startListener()
	e.waitIdle()
	after := e.scalar(`SELECT count(*) FROM deal_events`)
	// GRN = 1 event; QC = one TxEventBatch of QualityRecorded + TrancheAccepted + MseClockStarted.
	if after != before+4 || e.scalar(`SELECT count(DISTINCT tx_id) FROM deal_events`) != int64(e.mock.EventCount()) {
		t.Fatalf("resume must project exactly the 2 missed chain events (4 logical): before %d after %d chain %d", before, after, e.mock.EventCount())
	}
	if n := e.scalar(`SELECT count(DISTINCT (tx_id, event_name, event_seq)) FROM deal_events`); n != after {
		t.Fatalf("duplicate events after resume")
	}
	cpBlock := e.scalar(`SELECT block_no FROM chain_checkpoint WHERE listener_id = 'e2e'`)
	if uint64(cpBlock) != e.mock.Height() {
		t.Fatalf("checkpoint block %d, chain height %d", cpBlock, e.mock.Height())
	}
}

// TestTxEventBatchProjectedPerLogicalEvent: the chaincode wraps a transaction's several events in
// one TxEventBatch (Fabric keeps one chaincode event per transaction). Each logical event must be
// recorded, projected and published on its own, including two of the same name, and a full replay
// of the channel must not duplicate any of them.
func TestTxEventBatchProjectedPerLogicalEvent(t *testing.T) {
	e := setup(t)
	const id = "DL-20261020-0003"
	body := fmt.Sprintf(`{"dealId":%q,"poNumber":"PO-4412","buyerId":"BUYER-001","supplierId":"SUP-007",
		"deliveryDeadline":%q,"mse":{"category":"SMALL","termDays":45,"udyamNo":"UDYAM-KL-00-0000007","applies":true},
		"lines":[{"lineNo":1,"sku":"SKU-STEEL-12","orderedQty":50,"unitPricePaise":1000000},
		         {"lineNo":2,"sku":"SKU-STEEL-16","orderedQty":50,"unitPricePaise":1000000}]}`,
		id, e.clk.Now().Add(60*24*time.Hour).UTC().Format(time.RFC3339))
	e.must(201, e.do("POST", "/api/v1/deals", "BUYER", "buyer-procurement", body), "create two-line deal")
	e.must(200, e.do("POST", "/api/v1/deals/"+id+"/lien", "BANK", "bank-officer", map[string]any{"lienRef": "LIEN-9922", "amountPaise": 100_000_000}), "lien")
	e.must(200, e.internal("PostGoodsReceipt", id, "1", `[{"lineNo":1,"deliveredQty":50},{"lineNo":2,"deliveredQty":50}]`, "warehouse:GR-2"), "GRN")

	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/v1/events/stream?org=BUYER&dealId="+id, nil)
	res, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the defer below; read in a goroutine
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	live := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: ") {
				live <- strings.TrimPrefix(sc.Text(), "event: ")
			}
		}
		close(live)
	}()
	e.waitIdle()

	e.must(200, e.internal("RecordQualityCheck", id, "1", `[{"lineNo":1,"acceptedQty":40,"rejectedQty":10},{"lineNo":2,"acceptedQty":45,"rejectedQty":5}]`, "warehouse:QC-2"), "QC")
	e.waitIdle()
	const want = "QualityRecorded/0,HeldPortionCreated/1,HeldPortionCreated/2,TrancheAccepted/3,MseClockStarted/4"
	qcRows := func() string {
		return e.text(`SELECT string_agg(event_name || '/' || event_seq, ',' ORDER BY event_seq) FROM deal_events
			WHERE tx_id = (SELECT tx_id FROM deal_events WHERE deal_id = $1 AND event_name = 'QualityRecorded')`, id)
	}
	if got := qcRows(); got != want {
		t.Fatalf("QC batch rows: want %s, got %s", want, got)
	}
	if n := e.scalar(`SELECT count(*) FROM deal_events WHERE event_name = 'TxEventBatch'`); n != 0 {
		t.Fatalf("the batch envelope must be unwrapped, found %d raw rows", n)
	}
	if n := e.scalar(`SELECT count(*) FROM held_portions WHERE deal_id = $1`, id); n != 2 {
		t.Fatalf("two held portions expected, got %d", n)
	}
	if v := e.scalar(`SELECT accepted_value_paise FROM tranches WHERE deal_id = $1 AND tranche_no = 1`, id); v != 85_000_000 {
		t.Fatalf("accepted value %d", v)
	}
	// The stream first replays the deal's history; collect what follows the GRN.
	var got []string
	seenGRN := false
	for len(got) < 5 {
		select {
		case ev := <-live:
			if seenGRN {
				got = append(got, ev)
			}
			seenGRN = seenGRN || ev == "GoodsReceived"
		case <-time.After(5 * time.Second):
			t.Fatalf("SSE: got only %v", got)
		}
	}
	if fmt.Sprint(got) != "[QualityRecorded HeldPortionCreated HeldPortionCreated TrancheAccepted MseClockStarted]" {
		t.Fatalf("SSE must publish each logical event in order, got %v", got)
	}

	// Full replay from block 0: nothing is projected twice.
	rows := e.scalar(`SELECT count(*) FROM deal_events`)
	e.stopListener()
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM chain_checkpoint`); err != nil {
		t.Fatal(err)
	}
	e.startListener()
	e.waitIdle()
	e.eventually("replay reached chain head", func() bool {
		return uint64(e.scalar(`SELECT coalesce(max(block_no), 0) FROM chain_checkpoint`)) == e.mock.Height()
	})
	if n := e.scalar(`SELECT count(*) FROM deal_events`); n != rows || qcRows() != want {
		t.Fatalf("replay duplicated events: %d rows before, %d after (%s)", rows, n, qcRows())
	}
}

func TestSSEStreamAndReplay(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/v1/events/stream?org=BUYER&dealId="+demoID, nil)
	res, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the defer below; read in a goroutine
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %v", res.StatusCode, res.Header)
	}
	got := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: ") {
				got <- strings.TrimPrefix(sc.Text(), "event: ")
			}
		}
		close(got)
	}()
	e.createDemo()
	want := []string{"DealCreated", "LienConfirmed"}
	for _, w := range want {
		select {
		case ev := <-got:
			if ev != w {
				t.Fatalf("SSE: want %s, got %s", w, ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("SSE: no %s event", w)
		}
	}
	// Reconnect with Last-Event-ID = first event: only LienConfirmed is replayed.
	first := e.scalar(`SELECT min(id) FROM deal_events`)
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/v1/events/stream", nil)
	req2.Header.Set("X-Org", "AUDITOR")
	req2.Header.Set("Last-Event-ID", fmt.Sprint(first))
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res2.Body.Close() }()
	sc := bufio.NewScanner(res2.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event: ") {
			if ev := strings.TrimPrefix(sc.Text(), "event: "); ev != "LienConfirmed" {
				t.Fatalf("replay: want LienConfirmed, got %s", ev)
			}
			return
		}
	}
	t.Fatal("replay delivered nothing")
}

func TestMVCCConflictRetriedTransparently(t *testing.T) {
	e := setup(t)
	e.createDemo()
	e.mock.FailNextWithMVCC(3)
	e.must(200, e.internal("PostGoodsReceipt", demoID, "1", `[{"lineNo":1,"deliveredQty":10}]`, "warehouse:GR-1"), "GRN survives 3 MVCC conflicts")
	e.mock.FailNextWithMVCC(5)
	r := e.internal("PostGoodsReceipt", demoID, "2", `[{"lineNo":1,"deliveredQty":10}]`, "warehouse:GR-2")
	e.mustErr(503, "ERR_MVCC_CONFLICT", r, "5 conflicts exhaust the retry budget")
	if r.header.Get("Retry-After") == "" {
		t.Fatal("503 must carry Retry-After")
	}
}
