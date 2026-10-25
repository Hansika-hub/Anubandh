// Command demo runs the Anubandh live-demo scenario (BUILD.md section 15) THROUGH the ingest path:
// signed webhooks -> ingest-api -> Kafka -> ingest-worker -> gateway -> chain. The warehouse GRN is
// sent twice and every inbound event is shuffled, so the demo shows idempotency and out-of-order
// handling live. It exits non-zero on any deviation.
//
//	make up && make demo        (needs FABRIC_MOCK=true on the gateway for the clock fast-forward)
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"time"
)

var (
	gateway = flag.String("gateway", env("GATEWAY_PUBLIC_URL", "http://localhost:8080"), "gateway base URL")
	ingest  = flag.String("ingest", env("INGEST_PUBLIC_URL", "http://localhost:8081"), "ingest-api base URL")
	seed    = flag.Int64("seed", time.Now().UnixNano()%1_000_000, "shuffle seed (printed so a run can be repeated)")
	client  = &http.Client{Timeout: 30 * time.Second}
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "DEMO FAIL: "+format+"\n", a...)
	os.Exit(1)
}

type res struct {
	status int
	body   map[string]any
	raw    string
}

func do(req *http.Request) res {
	resp, err := client.Do(req)
	if err != nil {
		fail("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := res{status: resp.StatusCode, raw: string(raw)}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&r.body)
	return r
}

func gw(method, path, org, user string, body any) res {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, *gateway+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org", org)
	}
	if user != "" {
		req.Header.Set("X-User", user)
	}
	if t := os.Getenv("INTERNAL_TOKEN"); t != "" {
		req.Header.Set("X-Internal-Token", t)
	}
	return do(req)
}

func step(name string, want int, r res) res {
	if r.status != want {
		fail("%s: want HTTP %d, got %d: %s", name, want, r.status, r.raw)
	}
	fmt.Printf("  ok  %-62s HTTP %d\n", name, r.status)
	return r
}

func waitFor(name string, cond func() bool) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			fmt.Printf("  ok  %-62s\n", name)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	fail("timed out waiting for: %s", name)
}

func num(v any) int64 {
	if n, ok := v.(json.Number); ok {
		i, _ := n.Int64()
		return i
	}
	return -1
}

func deal(id string) map[string]any {
	r := gw("GET", "/api/v1/deals/"+id, "BUYER", "", nil)
	if r.status != 200 {
		fail("GET deal %s: %d %s", id, r.status, r.raw)
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

// event is one signed webhook.
type event struct {
	path, source string
	env          map[string]any
}

func envelope(typ, idem, dealID string, tr int, payload any) map[string]any {
	e := map[string]any{"eventType": typ, "idemKey": idem, "dealId": dealID, "payload": payload,
		"occurredAt": time.Now().UTC().Format(time.RFC3339)}
	if tr > 0 {
		e["trancheNo"] = tr
	}
	return e
}

func send(ev event) {
	secret := os.Getenv("HMAC_SECRET_" + upper(ev.source))
	if secret == "" {
		fail("HMAC_SECRET_%s is not set (source deploy/.env)", upper(ev.source))
	}
	body, _ := json.Marshal(ev.env)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req, _ := http.NewRequest("POST", *ingest+"/ingest/v1/events/"+ev.path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Source", ev.source)
	req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Idempotency-Key", ev.env["idemKey"].(string))
	if r := do(req); r.status != 202 {
		fail("ingest %s %v: HTTP %d %s", ev.path, ev.env["idemKey"], r.status, r.raw)
	}
	fmt.Printf("      -> %-14s %-22s accepted (202)\n", ev.path, ev.env["idemKey"])
}

func shuffled(rng *rand.Rand, evs []event) []event {
	out := append([]event(nil), evs...)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func main() {
	flag.Parse()
	rng := rand.New(rand.NewSource(*seed))
	now := time.Now().UTC()
	n := rng.Intn(4000) + 1000
	dealID := fmt.Sprintf("DL-%s-%04d", now.Format("20060102"), n)
	bigID := fmt.Sprintf("DL-%s-%04d", now.Format("20060102"), n+5000)
	tag := fmt.Sprint(now.UnixNano() % 1_000_000_000)
	fmt.Printf("Anubandh live demo via ingest (shuffle seed %d; rerun with -seed %d)\n", *seed, *seed)

	h := step("gateway health", 200, gw("GET", "/health", "", "", nil))
	if chain, _ := h.body["chain"].(map[string]any); chain["mode"] != "mock" {
		fail("the demo fast-forwards the clock and needs FABRIC_MOCK=true; gateway reports chain mode %v", chain["mode"])
	}
	r, err := client.Get(*ingest + "/ingest/v1/health")
	if err != nil || r.StatusCode != 200 {
		fail("ingest-api health: %v", err)
	}
	_ = r.Body.Close()
	fmt.Println("  ok  ingest-api health")

	deadline := now.Add(60 * 24 * time.Hour).Format(time.RFC3339)
	mk := func(id, supplier, cat string, term int, qty, price int64) map[string]any {
		return map[string]any{"dealId": id, "poNumber": "PO-4411", "buyerId": "BUYER-001", "supplierId": supplier, "deliveryDeadline": deadline,
			"mse":   map[string]any{"category": cat, "termDays": term, "udyamNo": "UDYAM-KL-00-0000007", "applies": true},
			"lines": []map[string]any{{"lineNo": 1, "sku": "SKU-STEEL-12", "orderedQty": qty, "unitPricePaise": price}}}
	}

	fmt.Println("\n1. Buyer creates the ₹10L deal (SUP-007)")
	step("create deal", 201, gw("POST", "/api/v1/deals", "BUYER", "buyer-procurement", mk(dealID, "SUP-007", "SMALL", 45, 100, 1000000)))

	fmt.Println("\n2. Bank lien, warehouse GRN (sent TWICE), QC 80/20 and GST invoice arrive SHUFFLED")
	grPayload := map[string]any{"lines": []map[string]any{{"lineNo": 1, "deliveredQty": 100}}}
	gr := event{"goods-receipt", "warehouse", envelope("GOODS_RECEIPT", "warehouse:GR-"+tag, dealID, 1, grPayload)}
	evs := []event{
		{"bank-lien", "bank", envelope("BANK_LIEN", "bank:LIEN-"+tag, dealID, 0, map[string]any{"lienRef": "LIEN-9921", "amountPaise": 100000000})},
		gr, gr, // duplicate delivery of the same GRN
		{"quality-check", "warehouse", envelope("QUALITY_CHECK", "warehouse:QC-"+tag, dealID, 1,
			map[string]any{"results": []map[string]any{{"lineNo": 1, "acceptedQty": 80, "rejectedQty": 20}}})},
		{"invoice", "gst", envelope("INVOICE", "gst:IRN-"+tag, dealID, 0, map[string]any{"irn": "IRN-" + tag})},
	}
	for _, e := range shuffled(rng, evs) {
		send(e)
	}
	waitFor("deal PARTIALLY_ACCEPTED with ₹2L held (80 of 100 units pass)", func() bool {
		d := deal(dealID)
		return d["status"] == "PARTIALLY_ACCEPTED" && num(d["totalHeldPaise"]) == 20000000
	})

	fmt.Println("\n3. Plant Head approves ₹8L -> released once, ₹2L stays held")
	ap := step("PLANT_HEAD approves tranche 1", 200, gw("POST", "/api/v1/deals/"+dealID+"/tranches/1/approve", "BUYER", "buyer-planthead", nil))
	if ap.body["satisfied"] != true {
		fail("approval policy should be satisfied: %s", ap.raw)
	}
	waitFor("tranche 1 RELEASED/SETTLED", func() bool {
		s := tranche(deal(dealID), 0)["status"]
		return s == "RELEASED" || s == "SETTLED"
	})

	fmt.Println("\n4. Supplier ships 20 replacements -> GRN/QC via ingest -> remaining ₹2L released")
	step("supplier ships 20 replacements", 200, gw("POST", "/api/v1/deals/"+dealID+"/held/1/1/replacement", "SUPPLIER", "supplier-007", map[string]any{"qty": 20}))
	gr2 := event{"goods-receipt", "warehouse", envelope("GOODS_RECEIPT", "warehouse:GR2-"+tag, dealID, 2, map[string]any{"lines": []map[string]any{{"lineNo": 1, "deliveredQty": 20}}})}
	qc2 := event{"quality-check", "warehouse", envelope("QUALITY_CHECK", "warehouse:QC2-"+tag, dealID, 2, map[string]any{"results": []map[string]any{{"lineNo": 1, "acceptedQty": 20, "rejectedQty": 0}}})}
	for _, e := range shuffled(rng, []event{gr2, qc2, gr2}) {
		send(e)
	}
	waitFor("tranche 2 accepted", func() bool {
		s := tranche(deal(dealID), 1)["status"]
		return s == "ACCEPTED" || s == "RELEASED" || s == "SETTLED"
	})
	step("PLANT_HEAD approves tranche 2", 200, gw("POST", "/api/v1/deals/"+dealID+"/tranches/2/approve", "BUYER", "buyer-planthead", nil))
	waitFor("₹10L released in total, exactly once", func() bool { return num(deal(dealID)["totalReleasedPaise"]) == 100000000 })

	fmt.Println("\n5. ₹60L MICRO deal: 15-day clock -> deemed acceptance -> PLANT_HEAD + CFO")
	step("create ₹60L deal (SUP-012, MICRO, 15 days)", 201, gw("POST", "/api/v1/deals", "BUYER", "buyer-procurement", mk(bigID, "SUP-012", "MICRO", 15, 60, 10000000)))
	for _, e := range shuffled(rng, []event{
		{"bank-lien", "bank", envelope("BANK_LIEN", "bank:LIEN2-"+tag, bigID, 0, map[string]any{"lienRef": "LIEN-2", "amountPaise": 600000000})},
		{"goods-receipt", "warehouse", envelope("GOODS_RECEIPT", "warehouse:GR3-"+tag, bigID, 1, map[string]any{"lines": []map[string]any{{"lineNo": 1, "deliveredQty": 60}}})},
	}) {
		send(e)
	}
	waitFor("₹60L tranche delivered", func() bool { return tranche(deal(bigID), 0)["status"] != nil })
	step("fast-forward the chain clock 15 days", 200, gw("POST", "/internal/v1/mock/clock", "", "", map[string]any{"advanceDays": 15}))
	waitFor("tranche DEEMED_ACCEPTED (supplier is not blocked by buyer silence)", func() bool { return tranche(deal(bigID), 0)["status"] == "DEEMED_ACCEPTED" })
	step("PLANT_HEAD approves ₹60L", 200, gw("POST", "/api/v1/deals/"+bigID+"/tranches/1/approve", "BUYER", "buyer-planthead", nil))
	if r := gw("POST", "/api/v1/deals/"+bigID+"/tranches/1/release", "BUYER", "buyer-planthead", nil); r.status != 409 {
		fail("release without CFO must be refused (409), got %d %s", r.status, r.raw)
	}
	fmt.Println("  ok  release without CFO refused (policy: ≥₹50L needs PLANT_HEAD + CFO)")
	step("CFO approves -> release", 200, gw("POST", "/api/v1/deals/"+bigID+"/tranches/1/approve", "BUYER", "buyer-cfo", nil))
	waitFor("₹60L tranche RELEASED/SETTLED", func() bool { s := tranche(deal(bigID), 0)["status"]; return s == "RELEASED" || s == "SETTLED" })

	fmt.Println("\n6. Nothing was lost: the dead-letter queue is empty")
	if tok := os.Getenv("INGEST_OPERATOR_TOKEN"); tok != "" {
		req, _ := http.NewRequest("GET", *ingest+"/ingest/v1/dlq", nil)
		req.Header.Set("X-Operator-Token", tok)
		d := do(req)
		if d.status != 200 || num(d.body["count"]) != 0 {
			fail("DLQ should be empty: HTTP %d %s", d.status, d.raw)
		}
		fmt.Println("  ok  DLQ empty")
	} else {
		fmt.Println("  --  INGEST_OPERATOR_TOKEN not set; skipped DLQ check")
	}
	fmt.Println("\nDEMO PASS")
}
