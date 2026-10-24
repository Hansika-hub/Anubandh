// Command smoke drives the BUILD.md §15 demo scenario against a running gateway and fails loudly
// on any deviation. It needs FABRIC_MOCK=true (it fast-forwards the mock clock) and a reachable
// sim-bank for settlement.
//
//	go run ./cmd/smoke -url http://localhost:8080
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"time"
)

var (
	base   = flag.String("url", "http://localhost:8080", "gateway base URL")
	token  = flag.String("internal-token", os.Getenv("INTERNAL_TOKEN"), "X-Internal-Token for /internal/v1")
	client = &http.Client{Timeout: 30 * time.Second}
)

type result struct {
	status int
	body   map[string]any
	raw    string
}

func call(method, path, org, user string, body any) result {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, *base+path, rdr)
	if err != nil {
		fail("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org", org)
	}
	if user != "" {
		req.Header.Set("X-User", user)
	}
	if *token != "" {
		req.Header.Set("X-Internal-Token", *token)
	}
	res, err := client.Do(req)
	if err != nil {
		fail("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	r := result{status: res.StatusCode, raw: string(raw)}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&r.body)
	return r
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "SMOKE FAIL: "+format+"\n", args...)
	os.Exit(1)
}

func step(name string, want int, r result) result {
	if r.status != want {
		fail("%s: want HTTP %d, got %d: %s", name, want, r.status, r.raw)
	}
	fmt.Printf("  ok  %-58s HTTP %d\n", name, r.status)
	return r
}

func code(r result) string {
	if e, ok := r.body["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func expectErr(name string, want int, wantCode string, r result) {
	if r.status != want || code(r) != wantCode {
		fail("%s: want %d %s, got %d %s", name, want, wantCode, r.status, r.raw)
	}
	fmt.Printf("  ok  %-58s %d %s\n", name, r.status, wantCode)
}

func num(v any) int64 {
	if n, ok := v.(json.Number); ok {
		i, _ := n.Int64()
		return i
	}
	return -1
}

func internal(fn string, args ...string) result {
	return call("POST", "/internal/v1/submit", "", "", map[string]any{"fn": fn, "args": args, "idemKey": args[len(args)-1]})
}

func waitFor(name string, cond func() bool) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			fmt.Printf("  ok  %-58s\n", name)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	fail("timed out: %s", name)
}

// deal reads a deal quietly (it is polled while waiting for projections).
func deal(id string) map[string]any {
	r := call("GET", "/api/v1/deals/"+id, "BUYER", "", nil)
	if r.status != http.StatusOK {
		fail("GET deal %s: HTTP %d: %s", id, r.status, r.raw)
	}
	return r.body
}

func tranche(d map[string]any, i int) map[string]any {
	ts, _ := d["tranches"].([]any)
	if len(ts) <= i {
		return map[string]any{}
	}
	t, _ := ts[i].(map[string]any)
	return t
}

func main() {
	flag.Parse()
	now := time.Now().UTC()
	seq := rand.IntN(4000) + 1000 //nolint:gosec // demo id, not a secret
	dealID := fmt.Sprintf("DL-%s-%04d", now.Format("20060102"), seq)
	bigID := fmt.Sprintf("DL-%s-%04d", now.Format("20060102"), seq+5000)
	tag := fmt.Sprint(now.UnixNano())
	fmt.Printf("Anubandh gateway smoke test against %s (deals %s, %s)\n", *base, dealID, bigID)

	h := step("health", 200, call("GET", "/health", "", "", nil))
	if chain, _ := h.body["chain"].(map[string]any); chain["mode"] != "mock" {
		fail("smoke needs FABRIC_MOCK=true, gateway reports chain mode %v", chain["mode"])
	}

	deadline := now.Add(60 * 24 * time.Hour).Format(time.RFC3339)
	mk := func(id, supplier, cat string, term int, qty, price int64) map[string]any {
		return map[string]any{"dealId": id, "poNumber": "PO-4411", "buyerId": "BUYER-001", "supplierId": supplier, "deliveryDeadline": deadline,
			"mse":   map[string]any{"category": cat, "termDays": term, "udyamNo": "UDYAM-KL-00-0000007", "applies": true},
			"lines": []map[string]any{{"lineNo": 1, "sku": "SKU-STEEL-12", "orderedQty": qty, "unitPricePaise": price}}}
	}
	fmt.Println("1-2. Buyer creates the ₹10L deal, bank confirms the lien")
	step("create deal (SUP-007, SMALL, 45 days)", 201, call("POST", "/api/v1/deals", "BUYER", "buyer-procurement", mk(dealID, "SUP-007", "SMALL", 45, 100, 1000000)))
	step("bank confirms lien ₹10L", 200, call("POST", "/api/v1/deals/"+dealID+"/lien", "BANK", "bank-officer", map[string]any{"lienRef": "LIEN-9921", "amountPaise": 100000000}))

	fmt.Println("3. GRN 100 units, duplicated and shuffled with QC")
	expectErr("QC before GRN is rejected (worker requeues)", 409, "ERR_INVALID_TRANSITION", internal("RecordQualityCheck", dealID, "1", `[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]`, "warehouse:QC-"+tag))
	step("GRN 100 units", 200, internal("PostGoodsReceipt", dealID, "1", `[{"lineNo":1,"deliveredQty":100}]`, "warehouse:GR-"+tag))
	expectErr("duplicate GRN is rejected", 409, "ERR_DUPLICATE", internal("PostGoodsReceipt", dealID, "1", `[{"lineNo":1,"deliveredQty":100}]`, "warehouse:GR-"+tag))

	fmt.Println("4. QC: 80 accepted / 20 rejected")
	step("QC 80/20 (requeued event)", 200, internal("RecordQualityCheck", dealID, "1", `[{"lineNo":1,"acceptedQty":80,"rejectedQty":20}]`, "warehouse:QC-"+tag))
	waitFor("deal PARTIALLY_ACCEPTED, ₹2L held", func() bool {
		d := deal(dealID)
		return d["status"] == "PARTIALLY_ACCEPTED" && num(d["totalHeldPaise"]) == 20000000
	})

	fmt.Println("5. Plant Head approves -> auto release over NEFT -> settled")
	ap := step("PLANT_HEAD approves ₹8L", 200, call("POST", "/api/v1/deals/"+dealID+"/tranches/1/approve", "BUYER", "buyer-planthead", nil))
	if ap.body["satisfied"] != true {
		fail("approval policy should be satisfied: %s", ap.raw)
	}
	waitFor("tranche 1 SETTLED via NEFT", func() bool {
		t := tranche(deal(dealID), 0)
		return t["status"] == "SETTLED" && t["rail"] == "NEFT" && t["bankRef"] != nil
	})

	fmt.Println("6. Supplier ships 20 replacements -> new tranche -> released")
	step("supplier ships 20 replacements", 200, call("POST", "/api/v1/deals/"+dealID+"/held/1/1/replacement", "SUPPLIER", "supplier-007", map[string]any{"qty": 20}))
	step("GRN tranche 2", 200, internal("PostGoodsReceipt", dealID, "2", `[{"lineNo":1,"deliveredQty":20}]`, "warehouse:GR2-"+tag))
	step("QC tranche 2: 20/0", 200, internal("RecordQualityCheck", dealID, "2", `[{"lineNo":1,"acceptedQty":20,"rejectedQty":0}]`, "warehouse:QC2-"+tag))
	step("PLANT_HEAD approves tranche 2", 200, call("POST", "/api/v1/deals/"+dealID+"/tranches/2/approve", "BUYER", "buyer-planthead", nil))
	waitFor("deal SETTLED, ₹10L released exactly once", func() bool {
		d := deal(dealID)
		return d["status"] == "SETTLED" && num(d["totalReleasedPaise"]) == 100000000
	})

	fmt.Println("7. ₹60L MICRO deal: fast-forward 15 days -> deemed acceptance, PLANT_HEAD + CFO")
	step("create ₹60L deal (SUP-012, MICRO, 15 days)", 201, call("POST", "/api/v1/deals", "BUYER", "buyer-procurement", mk(bigID, "SUP-012", "MICRO", 15, 60, 10000000)))
	step("bank confirms lien ₹60L", 200, call("POST", "/api/v1/deals/"+bigID+"/lien", "BANK", "bank-officer", map[string]any{"lienRef": "LIEN-2", "amountPaise": 600000000}))
	step("GRN 60 units, no QC", 200, internal("PostGoodsReceipt", bigID, "1", `[{"lineNo":1,"deliveredQty":60}]`, "warehouse:GR3-"+tag))
	step("fast-forward 15 days", 200, call("POST", "/internal/v1/mock/clock", "", "", map[string]any{"advanceDays": 15}))
	waitFor("tranche DEEMED_ACCEPTED", func() bool { return tranche(deal(bigID), 0)["status"] == "DEEMED_ACCEPTED" })
	step("PLANT_HEAD approves ₹60L", 200, call("POST", "/api/v1/deals/"+bigID+"/tranches/1/approve", "BUYER", "buyer-planthead", nil))
	expectErr("release without CFO is refused", 409, "ERR_POLICY_NOT_SATISFIED", call("POST", "/api/v1/deals/"+bigID+"/tranches/1/release", "BUYER", "buyer-planthead", nil))
	step("CFO approves -> auto release", 200, call("POST", "/api/v1/deals/"+bigID+"/tranches/1/approve", "BUYER", "buyer-cfo", nil))
	waitFor("₹60L tranche SETTLED", func() bool { return tranche(deal(bigID), 0)["status"] == "SETTLED" })

	fmt.Println("Read models")
	step("MSE clocks", 200, call("GET", "/api/v1/mse/clocks", "BUYER", "", nil))
	step("auditor compliance report", 200, call("GET", "/api/v1/mse/compliance-report", "AUDITOR", "auditor", nil))
	step("payment outbox", 200, call("GET", "/api/v1/payments/outbox?dealId="+dealID, "BANK", "", nil))
	step("ledger history", 200, call("GET", "/api/v1/deals/"+dealID+"/history", "AUDITOR", "auditor", nil))
	fmt.Println("SMOKE PASS")
}
