package gateways

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test harness
// ─────────────────────────────────────────────────────────────────────────────

// newTestContext builds an openruntimes.Context the way the runtime would.
// The logger is disabled so nothing tries to write to /mnt/logs.
func newTestContext(t *testing.T, path, body string, headers map[string]string) openruntimes.Context {
	t.Helper()

	logger, err := openruntimes.NewLogger("disabled", "")
	if err != nil {
		t.Fatalf("building logger: %v", err)
	}

	ctx := openruntimes.NewContext(logger)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["content-type"] = "application/json"
	ctx.Req.Headers = headers
	ctx.Req.Method = http.MethodPost
	ctx.Req.Path = path
	ctx.Req.SetBodyBinary([]byte(body))
	return ctx
}

// decodeResponse pulls the status code and parsed JSON body out of a response.
func decodeResponse(t *testing.T, res openruntimes.Response) (int, map[string]interface{}) {
	t.Helper()

	out := map[string]interface{}{}
	if len(res.Body) > 0 {
		if err := json.Unmarshal(res.Body, &out); err != nil {
			t.Fatalf("response body is not JSON: %v (body=%s)", err, res.Body)
		}
	}

	status := res.StatusCode
	if status == 0 {
		status = http.StatusOK // the SDK leaves 200 implicit
	}
	return status, out
}

// errorCode extracts error.code from a failure envelope.
func errorCode(body map[string]interface{}) string {
	errObj, ok := body["error"].(map[string]interface{})
	if !ok {
		return ""
	}
	code, _ := errObj["code"].(string)
	return code
}

// dataOf extracts the data object from a success envelope.
func dataOf(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, ok := body["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("response has no data object: %v", body)
	}
	return data
}

// stubStripe stands in for api.stripe.com. It records every request and replies
// with the supplied JSON.
type stubStripe struct {
	server   *httptest.Server
	response string
	status   int

	lastPath   string
	lastForm   url.Values
	lastHeader http.Header
}

func newStubStripe(t *testing.T, response string) *stubStripe {
	t.Helper()

	stub := &stubStripe{response: response, status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))

		stub.lastPath = r.URL.Path
		stub.lastForm = form
		stub.lastHeader = r.Header.Clone()

		// A GET (session retrieve) carries its parameters in the query string.
		if r.Method == http.MethodGet {
			stub.lastForm = r.URL.Query()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stub.status)
		fmt.Fprint(w, stub.response)
	}))

	stripeBackendURL = stub.server.URL
	t.Cleanup(func() {
		stub.server.Close()
		stripeBackendURL = ""
	})

	return stub
}

// setStripeEnv applies environment variables for one test and restores them after.
func setStripeEnv(t *testing.T, vars map[string]string) {
	t.Helper()

	base := map[string]string{
		"STRIPE_MODE":                "test",
		"PAYMENT_MODE":               "test",
		"STRIPE_SECRET_KEY":          "sk_test_dummy",
		"PAYMENT_SUCCESS_URL":        "https://app.example.com/success",
		"PAYMENT_CANCEL_URL":         "https://app.example.com/cancel",
		"STRIPE_WEBHOOK_SECRET":      "",
		"STRIPE_TEST_SECRET_KEY":     "",
		"STRIPE_LIVE_SECRET_KEY":     "",
		"PAYMENT_ALLOW_KEY_MISMATCH": "",
		"STRIPE_MIN_AMOUNT":          "",
		"PAYMENT_MAX_AMOUNT":         "",
		"PAYMENT_TEST_SUCCESS_URL":   "",
		"PAYMENT_TEST_CANCEL_URL":    "",
	}
	for k, v := range vars {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
}

const sessionCreatedJSON = `{
  "id": "cs_test_abc123",
  "object": "checkout.session",
  "url": "https://checkout.stripe.com/c/pay/cs_test_abc123",
  "amount_total": 1500,
  "currency": "usd",
  "payment_status": "unpaid",
  "status": "open",
  "expires_at": 1767225600,
  "metadata": {"order_id": "order_123"}
}`

// ─────────────────────────────────────────────────────────────────────────────
// Create — validation
// ─────────────────────────────────────────────────────────────────────────────

func TestStripeCreateValidation(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "empty body",
			body:       "",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "malformed JSON",
			body:       `{"amount": }`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "missing orderId",
			body:       `{"amount":1500,"currency":"USD"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			// The old code defaulted this to "usd", which silently turned a
			// BDT-priced order into a dollar charge.
			name:       "missing currency is rejected, not defaulted",
			body:       `{"amount":1500,"orderId":"order_123"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "invalid currency code",
			body:       `{"amount":1500,"currency":"DOLLARS","orderId":"order_123"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "zero amount",
			body:       `{"amount":0,"currency":"USD","orderId":"order_123"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "negative amount",
			body:       `{"amount":-500,"currency":"USD","orderId":"order_123"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "below Stripe minimum",
			body:       `{"amount":10,"currency":"USD","orderId":"order_123"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "amount_too_small",
		},
		{
			// Catches a client sending an already-converted amount twice.
			name:       "absurdly large amount",
			body:       `{"amount":999999999999,"currency":"USD","orderId":"order_123"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "amount_too_large",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setStripeEnv(t, nil)
			newStubStripe(t, sessionCreatedJSON)

			ctx := newTestContext(t, "/stripe/create", tc.body, nil)
			status, body := decodeResponse(t, HandleStripe(ctx))

			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d (body=%v)", status, tc.wantStatus, body)
			}
			if got := errorCode(body); got != tc.wantCode {
				t.Errorf("error code = %q, want %q", got, tc.wantCode)
			}
			if body["success"] != false {
				t.Errorf("success = %v, want false", body["success"])
			}
		})
	}
}

func TestStripeCreateRejectsLiveKeyInTestMode(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_SECRET_KEY": "sk_live_realkey"})
	newStubStripe(t, sessionCreatedJSON)

	ctx := newTestContext(t, "/stripe/create", `{"amount":1500,"currency":"USD","orderId":"o1"}`, nil)
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%v)", status, body)
	}
	if got := errorCode(body); got != "config_error" {
		t.Errorf("error code = %q, want config_error", got)
	}
}

func TestStripeCreateRequiresRedirectURLs(t *testing.T) {
	setStripeEnv(t, map[string]string{"PAYMENT_SUCCESS_URL": "", "PAYMENT_CANCEL_URL": ""})
	newStubStripe(t, sessionCreatedJSON)

	ctx := newTestContext(t, "/stripe/create", `{"amount":1500,"currency":"USD","orderId":"o1"}`, nil)
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
	if got := errorCode(body); got != "config_error" {
		t.Errorf("error code = %q, want config_error", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Create — happy path and request shape
// ─────────────────────────────────────────────────────────────────────────────

// Stripe enables Managed Payments by default, which rejects line items with no
// tax_code. The client already sends one line item whose amount includes the
// tax it calculated, so the session must opt out rather than let Stripe
// calculate tax a second time.
func TestStripeCreateOptsOutOfManagedPayments(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, sessionCreatedJSON)

	body := `{"amount": 1500, "currency": "usd", "orderId": "order_123"}`
	ctx := newTestContext(t, "/stripe/create", body, nil)
	status, resp := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, resp)
	}
	if got := stub.lastForm.Get("managed_payments[enabled]"); got != "false" {
		t.Errorf("managed_payments[enabled] = %q, want %q", got, "false")
	}
}

// An account that genuinely wants Stripe managing tax opts back in, and must
// then supply its own tax codes. The parameter must be absent entirely, not
// sent as "true", so Stripe falls back to the account default.
func TestStripeCreateHonoursManagedPaymentsOptIn(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_MANAGED_PAYMENTS": "true"})
	stub := newStubStripe(t, sessionCreatedJSON)

	body := `{"amount": 1500, "currency": "usd", "orderId": "order_123"}`
	ctx := newTestContext(t, "/stripe/create", body, nil)
	status, resp := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, resp)
	}
	if _, present := stub.lastForm["managed_payments[enabled]"]; present {
		t.Errorf("managed_payments[enabled] was sent despite STRIPE_MANAGED_PAYMENTS=true")
	}
}

func TestStripeCreateSendsCorrectParams(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, sessionCreatedJSON)

	body := `{
		"amount": 1500,
		"currency": "usd",
		"orderId": "order_123",
		"productName": "Chicken Biryani x2",
		"customerEmail": "customer@example.com"
	}`

	ctx := newTestContext(t, "/stripe/create", body, nil)
	status, resp := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, resp)
	}
	if resp["success"] != true {
		t.Fatalf("success = %v, want true", resp["success"])
	}

	if stub.lastPath != "/v1/checkout/sessions" {
		t.Errorf("path = %q, want /v1/checkout/sessions", stub.lastPath)
	}

	form := stub.lastForm
	checks := map[string]string{
		"mode":                                          "payment",
		"line_items[0][price_data][currency]":           "usd",
		"line_items[0][price_data][unit_amount]":        "1500",
		"line_items[0][price_data][product_data][name]": "Chicken Biryani x2",
		"line_items[0][quantity]":                       "1",
		"cancel_url":                                    "https://app.example.com/cancel",
		"client_reference_id":                           "order_123",
		"customer_email":                                "customer@example.com",
		"metadata[order_id]":                            "order_123",
		// The fix that matters most for reconciliation: the order ID must also
		// land on the PaymentIntent, which is what the dashboard's Payments
		// list and every refund actually reference.
		"payment_intent_data[metadata][order_id]": "order_123",
	}
	for key, want := range checks {
		if got := form.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}

	// Idempotency protects against a retried create opening a second session.
	if got := stub.lastHeader.Get("Idempotency-Key"); got != "stripe:create:order_123:1500:USD" {
		t.Errorf("Idempotency-Key = %q, want stripe:create:order_123:1500:USD", got)
	}

	data := dataOf(t, resp)
	if data["paymentURL"] != "https://checkout.stripe.com/c/pay/cs_test_abc123" {
		t.Errorf("paymentURL = %v", data["paymentURL"])
	}
	if data["sessionId"] != "cs_test_abc123" {
		t.Errorf("sessionId = %v", data["sessionId"])
	}
	if data["amountDisplay"] != "15.00" {
		t.Errorf("amountDisplay = %v, want 15.00", data["amountDisplay"])
	}
}

func TestStripeCreateProductNameDefaultsToOrderID(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, sessionCreatedJSON)

	ctx := newTestContext(t, "/stripe/create",
		`{"amount":1500,"currency":"USD","orderId":"order_123"}`, nil)
	if status, body := decodeResponse(t, HandleStripe(ctx)); status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, body)
	}

	got := stub.lastForm.Get("line_items[0][price_data][product_data][name]")
	if got != "Order order_123" {
		t.Errorf("product name = %q, want %q", got, "Order order_123")
	}
}

// TestStripeCreateSuccessURLQueryString covers the bug where a success URL that
// already had a query string produced "?ref=abc?session_id=...", burying
// session_id inside the previous parameter's value.
func TestStripeCreateSuccessURLQueryString(t *testing.T) {
	cases := []struct {
		name       string
		successURL string
		want       string
	}{
		{
			name:       "no existing query",
			successURL: "https://app.example.com/success",
			want:       "https://app.example.com/success?session_id={CHECKOUT_SESSION_ID}",
		},
		{
			name:       "existing query uses ampersand",
			successURL: "https://app.example.com/success?ref=abc",
			want:       "https://app.example.com/success?ref=abc&session_id={CHECKOUT_SESSION_ID}",
		},
		{
			name:       "multiple existing params",
			successURL: "https://app.example.com/success?ref=abc&utm=xyz",
			want:       "https://app.example.com/success?ref=abc&utm=xyz&session_id={CHECKOUT_SESSION_ID}",
		},
		{
			name:       "custom scheme deep link",
			successURL: "myapp://payment/success",
			want:       "myapp://payment/success?session_id={CHECKOUT_SESSION_ID}",
		},
		{
			name:       "fragment stays last",
			successURL: "https://app.example.com/success#done",
			want:       "https://app.example.com/success?session_id={CHECKOUT_SESSION_ID}#done",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setStripeEnv(t, map[string]string{"PAYMENT_SUCCESS_URL": tc.successURL})
			stub := newStubStripe(t, sessionCreatedJSON)

			ctx := newTestContext(t, "/stripe/create",
				`{"amount":1500,"currency":"USD","orderId":"order_123"}`, nil)
			if status, body := decodeResponse(t, HandleStripe(ctx)); status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%v)", status, body)
			}

			if got := stub.lastForm.Get("success_url"); got != tc.want {
				t.Errorf("success_url = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripeCreateMapsSDKErrorStatus(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, `{"error":{"type":"invalid_request_error","code":"parameter_invalid_integer","message":"Invalid integer: amount"}}`)
	stub.status = http.StatusBadRequest

	ctx := newTestContext(t, "/stripe/create",
		`{"amount":1500,"currency":"USD","orderId":"order_123"}`, nil)
	status, body := decodeResponse(t, HandleStripe(ctx))

	// A Stripe rejection must surface as a real HTTP error, not a 200 with
	// success:false buried in the body.
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body=%v)", status, body)
	}
	if body["success"] != false {
		t.Errorf("success = %v, want false", body["success"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Verify
// ─────────────────────────────────────────────────────────────────────────────

func sessionJSON(paymentStatus, status string) string {
	return fmt.Sprintf(`{
	  "id": "cs_test_abc123",
	  "object": "checkout.session",
	  "amount_total": 1500,
	  "currency": "usd",
	  "payment_status": %q,
	  "status": %q,
	  "client_reference_id": "order_123",
	  "metadata": {"order_id": "order_123"},
	  "payment_intent": {"id": "pi_test_123", "object": "payment_intent"},
	  "customer_details": {"email": "customer@example.com"}
	}`, paymentStatus, status)
}

func TestStripeVerifyOutcomes(t *testing.T) {
	cases := []struct {
		name          string
		paymentStatus string
		status        string
		wantStatus    string
		wantPaid      bool
	}{
		{"paid", "paid", "complete", "paid", true},
		{"zero-value order", "no_payment_required", "complete", "no_payment_required", true},
		{"still open", "unpaid", "open", "pending", false},
		{"expired without paying", "unpaid", "expired", "expired", false},
		// Checkout finished but the bank debit has not cleared. Releasing the
		// order here would be shipping against a payment that may still fail.
		{"async payment settling", "unpaid", "complete", "processing", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setStripeEnv(t, nil)
			newStubStripe(t, sessionJSON(tc.paymentStatus, tc.status))

			ctx := newTestContext(t, "/stripe/verify", `{"sessionId":"cs_test_abc123"}`, nil)
			httpStatus, body := decodeResponse(t, HandleStripe(ctx))

			if httpStatus != http.StatusOK {
				t.Fatalf("http status = %d, want 200 (body=%v)", httpStatus, body)
			}

			data := dataOf(t, body)
			if data["status"] != tc.wantStatus {
				t.Errorf("status = %v, want %v", data["status"], tc.wantStatus)
			}
			if data["paid"] != tc.wantPaid {
				t.Errorf("paid = %v, want %v", data["paid"], tc.wantPaid)
			}
			if data["orderId"] != "order_123" {
				t.Errorf("orderId = %v, want order_123", data["orderId"])
			}
		})
	}
}

func TestStripeVerifyReturnsPaymentDetails(t *testing.T) {
	setStripeEnv(t, nil)
	newStubStripe(t, sessionJSON("paid", "complete"))

	ctx := newTestContext(t, "/stripe/verify", `{"sessionId":"cs_test_abc123"}`, nil)
	_, body := decodeResponse(t, HandleStripe(ctx))
	data := dataOf(t, body)

	if data["paymentIntentId"] != "pi_test_123" {
		t.Errorf("paymentIntentId = %v, want pi_test_123", data["paymentIntentId"])
	}
	if data["customerEmail"] != "customer@example.com" {
		t.Errorf("customerEmail = %v", data["customerEmail"])
	}
	if data["amountDisplay"] != "15.00" {
		t.Errorf("amountDisplay = %v, want 15.00", data["amountDisplay"])
	}
	if data["currency"] != "USD" {
		t.Errorf("currency = %v, want USD", data["currency"])
	}
}

func TestStripeVerifyRejectsOrderMismatch(t *testing.T) {
	setStripeEnv(t, nil)
	newStubStripe(t, sessionJSON("paid", "complete"))

	ctx := newTestContext(t, "/stripe/verify",
		`{"sessionId":"cs_test_abc123","orderId":"someone_elses_order"}`, nil)
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%v)", status, body)
	}
	if got := errorCode(body); got != "order_mismatch" {
		t.Errorf("error code = %q, want order_mismatch", got)
	}
}

func TestStripeVerifyValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"missing sessionId", `{}`},
		{"not a session id", `{"sessionId":"pi_test_123"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setStripeEnv(t, nil)
			newStubStripe(t, sessionJSON("paid", "complete"))

			ctx := newTestContext(t, "/stripe/verify", tc.body, nil)
			status, body := decodeResponse(t, HandleStripe(ctx))

			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%v)", status, body)
			}
		})
	}
}

func TestStripeVerifyExpandsPaymentIntent(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, sessionJSON("paid", "complete"))

	ctx := newTestContext(t, "/stripe/verify", `{"sessionId":"cs_test_abc123"}`, nil)
	HandleStripe(ctx)

	if got := stub.lastForm.Get("expand[0]"); got != "payment_intent" {
		t.Errorf("expand[0] = %q, want payment_intent", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhook
// ─────────────────────────────────────────────────────────────────────────────

// signStripePayload builds a valid Stripe-Signature header for a payload.
func signStripePayload(payload, secret string, ts time.Time) string {
	signedPayload := fmt.Sprintf("%d.%s", ts.Unix(), payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func webhookEventJSON(eventType string, livemode bool, paymentStatus, status string) string {
	return fmt.Sprintf(`{
	  "id": "evt_test_123",
	  "object": "event",
	  "type": %q,
	  "livemode": %t,
	  "data": {"object": %s}
	}`, eventType, livemode, sessionJSON(paymentStatus, status))
}

func TestStripeWebhookAcceptsValidSignature(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_testsecret"})

	payload := webhookEventJSON("checkout.session.completed", false, "paid", "complete")
	signature := signStripePayload(payload, "whsec_testsecret", time.Now())

	ctx := newTestContext(t, "/stripe/webhook", payload,
		map[string]string{"stripe-signature": signature})
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, body)
	}

	data := dataOf(t, body)
	if data["received"] != true {
		t.Errorf("received = %v, want true", data["received"])
	}
	if data["handled"] != true {
		t.Errorf("handled = %v, want true", data["handled"])
	}

	payment, ok := data["payment"].(map[string]interface{})
	if !ok {
		t.Fatalf("payment summary missing: %v", data)
	}
	if payment["orderId"] != "order_123" {
		t.Errorf("orderId = %v, want order_123", payment["orderId"])
	}
	if payment["paid"] != true {
		t.Errorf("paid = %v, want true", payment["paid"])
	}
}

func TestStripeWebhookRejectsBadSignature(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_testsecret"})

	payload := webhookEventJSON("checkout.session.completed", false, "paid", "complete")
	forged := signStripePayload(payload, "whsec_wrongsecret", time.Now())

	ctx := newTestContext(t, "/stripe/webhook", payload,
		map[string]string{"stripe-signature": forged})
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%v)", status, body)
	}
	if got := errorCode(body); got != "invalid_signature" {
		t.Errorf("error code = %q, want invalid_signature", got)
	}
}

// A captured webhook replayed hours later must be refused, which is what the
// timestamp inside the signature is for.
func TestStripeWebhookRejectsReplayedEvent(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_testsecret"})

	payload := webhookEventJSON("checkout.session.completed", false, "paid", "complete")
	stale := signStripePayload(payload, "whsec_testsecret", time.Now().Add(-24*time.Hour))

	ctx := newTestContext(t, "/stripe/webhook", payload,
		map[string]string{"stripe-signature": stale})
	status, _ := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a replayed event", status)
	}
}

func TestStripeWebhookRejectsMissingSignature(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_testsecret"})

	payload := webhookEventJSON("checkout.session.completed", false, "paid", "complete")
	ctx := newTestContext(t, "/stripe/webhook", payload, nil)
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if got := errorCode(body); got != "invalid_signature" {
		t.Errorf("error code = %q, want invalid_signature", got)
	}
}

func TestStripeWebhookRequiresSecret(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": ""})

	payload := webhookEventJSON("checkout.session.completed", false, "paid", "complete")
	ctx := newTestContext(t, "/stripe/webhook", payload,
		map[string]string{"stripe-signature": "t=1,v1=deadbeef"})
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
	if got := errorCode(body); got != "config_error" {
		t.Errorf("error code = %q, want config_error", got)
	}
}

// A live-mode event reaching a test deployment means the endpoint is registered
// in the wrong Stripe dashboard; acting on it would mix real and test orders.
func TestStripeWebhookRejectsModeMismatch(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_testsecret"})

	payload := webhookEventJSON("checkout.session.completed", true, "paid", "complete")
	signature := signStripePayload(payload, "whsec_testsecret", time.Now())

	ctx := newTestContext(t, "/stripe/webhook", payload,
		map[string]string{"stripe-signature": signature})
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%v)", status, body)
	}
	if got := errorCode(body); got != "mode_mismatch" {
		t.Errorf("error code = %q, want mode_mismatch", got)
	}
}

// Unknown event types must still be acknowledged, or Stripe retries them for
// three days and eventually disables the endpoint.
func TestStripeWebhookAcknowledgesUnhandledEvents(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_testsecret"})

	payload := webhookEventJSON("customer.created", false, "paid", "complete")
	signature := signStripePayload(payload, "whsec_testsecret", time.Now())

	ctx := newTestContext(t, "/stripe/webhook", payload,
		map[string]string{"stripe-signature": signature})
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, body)
	}
	data := dataOf(t, body)
	if data["received"] != true {
		t.Errorf("received = %v, want true", data["received"])
	}
	if data["handled"] != false {
		t.Errorf("handled = %v, want false", data["handled"])
	}
}

// Stripe stamps events with the account's API version, which is set in the
// dashboard and routinely differs from the version stripe-go pins. The SDK's
// default ConstructEvent rejects that outright — which would have rejected
// every genuine webhook in production. The signature is what authenticates the
// request, so a version difference must be tolerated.
func TestStripeWebhookToleratesAPIVersionMismatch(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_testsecret"})

	payload := fmt.Sprintf(`{
	  "id": "evt_test_123",
	  "object": "event",
	  "type": "checkout.session.completed",
	  "livemode": false,
	  "api_version": "2019-05-16",
	  "data": {"object": %s}
	}`, sessionJSON("paid", "complete"))

	signature := signStripePayload(payload, "whsec_testsecret", time.Now())

	ctx := newTestContext(t, "/stripe/webhook", payload,
		map[string]string{"stripe-signature": signature})
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an older account API version must not "+
			"reject a correctly signed event (body=%v)", status, body)
	}

	payment := dataOf(t, body)["payment"].(map[string]interface{})
	if payment["paid"] != true {
		t.Errorf("paid = %v, want true", payment["paid"])
	}
}

func TestStripeWebhookReportsFailedAsyncPayment(t *testing.T) {
	setStripeEnv(t, map[string]string{"STRIPE_WEBHOOK_SECRET": "whsec_testsecret"})

	payload := webhookEventJSON("checkout.session.async_payment_failed", false, "unpaid", "complete")
	signature := signStripePayload(payload, "whsec_testsecret", time.Now())

	ctx := newTestContext(t, "/stripe/webhook", payload,
		map[string]string{"stripe-signature": signature})
	_, body := decodeResponse(t, HandleStripe(ctx))

	payment := dataOf(t, body)["payment"].(map[string]interface{})
	if payment["paid"] != false {
		t.Errorf("paid = %v, want false for a failed async payment", payment["paid"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Routing and helpers
// ─────────────────────────────────────────────────────────────────────────────

func TestStripeUnknownSubRoute(t *testing.T) {
	setStripeEnv(t, nil)

	ctx := newTestContext(t, "/stripe/refund", `{}`, nil)
	status, body := decodeResponse(t, HandleStripe(ctx))

	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if got := errorCode(body); got != "unknown_route" {
		t.Errorf("error code = %q, want unknown_route", got)
	}
}

func TestAppendQueryParam(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "https://a.com/s", want: "https://a.com/s?session_id={ID}"},
		{in: "https://a.com/s?x=1", want: "https://a.com/s?x=1&session_id={ID}"},
		{in: "https://a.com/s?x=1&y=2", want: "https://a.com/s?x=1&y=2&session_id={ID}"},
		{in: "myapp://pay/success", want: "myapp://pay/success?session_id={ID}"},
		{in: "https://a.com/s#frag", want: "https://a.com/s?session_id={ID}#frag"},
		{in: "not-a-url", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := appendQueryParam(tc.in, "session_id", "{ID}")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got %q", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripeFormatAmount(t *testing.T) {
	cases := []struct {
		minor    int64
		currency string
		want     string
	}{
		{1500, "USD", "15.00"},
		{1500, "usd", "15.00"},
		{5, "USD", "0.05"},
		{0, "USD", "0.00"},
		{150000, "BDT", "1500.00"},
		// Zero-decimal: 1500 JPY is ¥1500, not ¥15.00.
		{1500, "JPY", "1500"},
		{1500, "KRW", "1500"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d_%s", tc.minor, tc.currency), func(t *testing.T) {
			if got := stripeFormatAmount(tc.minor, tc.currency); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripeModeResolution(t *testing.T) {
	cases := []struct {
		name        string
		stripeMode  string
		paymentMode string
		want        string
	}{
		{"defaults to test", "", "", "test"},
		{"global live", "", "live", "live"},
		{"global test", "", "test", "test"},
		{"gateway override wins", "test", "live", "test"},
		{"gateway live over global test", "live", "test", "live"},
		{"unrecognised value falls back to test", "", "banana", "test"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRIPE_MODE", tc.stripeMode)
			t.Setenv("PAYMENT_MODE", tc.paymentMode)

			if got := stripeMode(); got != tc.want {
				t.Errorf("stripeMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripeEnvPrefersModeSpecificKey(t *testing.T) {
	t.Setenv("PAYMENT_MODE", "test")
	t.Setenv("STRIPE_MODE", "test")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_generic")
	t.Setenv("STRIPE_TEST_SECRET_KEY", "sk_test_specific")

	if got := stripeEnv("SECRET_KEY"); got != "sk_test_specific" {
		t.Errorf("got %q, want the mode-specific key", got)
	}

	// Falls back to the unprefixed name so existing deployments keep working.
	t.Setenv("STRIPE_TEST_SECRET_KEY", "")
	if got := stripeEnv("SECRET_KEY"); got != "sk_test_generic" {
		t.Errorf("got %q, want the generic key", got)
	}
}

func TestStripeSecretKeyGuards(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		key      string
		override string
		wantErr  bool
	}{
		{"test mode with test key", "test", "sk_test_x", "", false},
		{"live mode with live key", "live", "sk_live_x", "", false},
		{"test mode with live key is refused", "test", "sk_live_x", "", true},
		{"live mode with test key is refused", "live", "sk_test_x", "", true},
		{"restricted keys are allowed through", "live", "rk_live_x", "", false},
		{"unrecognised format is allowed through", "live", "custom_key", "", false},
		{"override disables the guard", "test", "sk_live_x", "true", false},
		{"missing key is an error", "test", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PAYMENT_MODE", tc.mode)
			t.Setenv("STRIPE_MODE", tc.mode)
			t.Setenv("STRIPE_SECRET_KEY", tc.key)
			t.Setenv("STRIPE_TEST_SECRET_KEY", "")
			t.Setenv("STRIPE_LIVE_SECRET_KEY", "")
			t.Setenv("PAYMENT_ALLOW_KEY_MISMATCH", tc.override)

			_, err := stripeSecretKey()
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestStripeModeSpecificRedirectURLs(t *testing.T) {
	t.Setenv("PAYMENT_MODE", "test")
	t.Setenv("STRIPE_MODE", "test")
	t.Setenv("PAYMENT_SUCCESS_URL", "https://live.example.com/success")
	t.Setenv("PAYMENT_TEST_SUCCESS_URL", "https://staging.example.com/success")

	if got := stripeModeURL("SUCCESS_URL"); got != "https://staging.example.com/success" {
		t.Errorf("got %q, want the test-specific URL", got)
	}

	t.Setenv("PAYMENT_TEST_SUCCESS_URL", "")
	if got := stripeModeURL("SUCCESS_URL"); got != "https://live.example.com/success" {
		t.Errorf("got %q, want the generic URL", got)
	}
}

// Per-request URLs let one function serve several apps with different deep links.
func TestStripeCreateAcceptsPerRequestURLs(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, sessionCreatedJSON)

	body := `{
		"amount": 1500, "currency": "USD", "orderId": "order_123",
		"successURL": "myapp://pay/ok",
		"cancelURL": "myapp://pay/cancel"
	}`
	ctx := newTestContext(t, "/stripe/create", body, nil)
	if status, resp := decodeResponse(t, HandleStripe(ctx)); status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, resp)
	}

	if got := stub.lastForm.Get("success_url"); got != "myapp://pay/ok?session_id={CHECKOUT_SESSION_ID}" {
		t.Errorf("success_url = %q", got)
	}
	if got := stub.lastForm.Get("cancel_url"); got != "myapp://pay/cancel" {
		t.Errorf("cancel_url = %q", got)
	}
}

func TestStripeCreateCarriesCustomMetadata(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, sessionCreatedJSON)

	body := `{
		"amount": 1500, "currency": "USD", "orderId": "order_123",
		"metadata": {"restaurantId": "rest_9", "riderZone": "dhanmondi"}
	}`
	ctx := newTestContext(t, "/stripe/create", body, nil)
	if status, resp := decodeResponse(t, HandleStripe(ctx)); status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, resp)
	}

	for key, want := range map[string]string{
		"metadata[restaurantId]":                      "rest_9",
		"metadata[riderZone]":                         "dhanmondi",
		"payment_intent_data[metadata][restaurantId]": "rest_9",
		"metadata[order_id]":                          "order_123",
	} {
		if got := stub.lastForm.Get(key); got != want {
			t.Errorf("form[%q] = %q, want %q", key, got, want)
		}
	}
}

// A client must not be able to overwrite order_id through the metadata map.
func TestStripeCreateMetadataCannotOverrideOrderID(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, sessionCreatedJSON)

	body := `{
		"amount": 1500, "currency": "USD", "orderId": "order_123",
		"metadata": {"order_id": "spoofed_order"}
	}`
	ctx := newTestContext(t, "/stripe/create", body, nil)
	HandleStripe(ctx)

	if got := stub.lastForm.Get("metadata[order_id]"); got != "order_123" {
		t.Errorf("metadata[order_id] = %q, want order_123", got)
	}
}

func TestStripeCreateTrimsAndUppercasesCurrency(t *testing.T) {
	setStripeEnv(t, nil)
	stub := newStubStripe(t, sessionCreatedJSON)

	ctx := newTestContext(t, "/stripe/create",
		`{"amount":1500,"currency":" usd ","orderId":"order_123"}`, nil)
	if status, body := decodeResponse(t, HandleStripe(ctx)); status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, body)
	}

	// Stripe expects a lower-case currency on the wire.
	if got := stub.lastForm.Get("line_items[0][price_data][currency]"); got != "usd" {
		t.Errorf("currency = %q, want usd", got)
	}
	if got := strings.ToUpper(stub.lastHeader.Get("Idempotency-Key")); !strings.HasSuffix(got, ":USD") {
		t.Errorf("idempotency key = %q, want it to end with :USD", got)
	}
}
