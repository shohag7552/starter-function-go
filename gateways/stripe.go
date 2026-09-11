// Package gateways holds the payment gateway handlers for the function.
//
// Only Stripe is implemented, using Stripe Checkout (hosted). The customer
// enters their card on checkout.stripe.com, so no card data ever touches this
// function or the client app.
package gateways

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
	"github.com/stripe/stripe-go/v76/webhook"
)

// stripeBackendURL overrides the Stripe API base URL. It is empty in
// production — tests point it at a stub server.
var stripeBackendURL string

const (
	// stripeTimeout is deliberately below Appwrite's 15s function timeout. If
	// an outbound call could outlive the function, the container would be
	// killed mid-request: the client gets nothing back while Stripe may
	// already have created the session.
	stripeTimeout = 10 * time.Second

	// defaultMinAmount is Stripe's own floor (~$0.50) in minor units.
	defaultMinAmount int64 = 50

	// defaultMaxAmount is a sanity ceiling that catches a client which
	// converted an amount to minor units twice.
	defaultMaxAmount int64 = 100000000
)

// stripeZeroDecimalCurrencies have no minor unit: 1500 JPY is ¥1500, not
// ¥15.00. Getting this wrong misreports the amount by a factor of 100.
var stripeZeroDecimalCurrencies = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "JPY": true,
	"KMF": true, "KRW": true, "MGA": true, "PYG": true, "RWF": true,
	"UGX": true, "VND": true, "VUV": true, "XAF": true, "XOF": true,
	"XPF": true,
}

// ─────────────────────────────────────────────────────────────────────────────
// Mode and configuration
// ─────────────────────────────────────────────────────────────────────────────

// stripeMode reports "test" or "live". STRIPE_MODE overrides PAYMENT_MODE, and
// anything unrecognised — including a misspelling or an unset variable — falls
// back to "test", so a configuration slip can never start charging real cards.
func stripeMode() string {
	if m := normalizeMode(os.Getenv("STRIPE_MODE")); m != "" {
		return m
	}
	if m := normalizeMode(os.Getenv("PAYMENT_MODE")); m != "" {
		return m
	}
	return "test"
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "test":
		return "test"
	case "live":
		return "live"
	default:
		return ""
	}
}

// stripeEnv reads a Stripe variable, preferring the mode-specific name
// (STRIPE_TEST_SECRET_KEY) over the plain one (STRIPE_SECRET_KEY). Test and
// live credentials can therefore sit side by side in the same environment.
func stripeEnv(suffix string) string {
	modeSpecific := "STRIPE_" + strings.ToUpper(stripeMode()) + "_" + suffix
	if v := strings.TrimSpace(os.Getenv(modeSpecific)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("STRIPE_" + suffix))
}

// stripeModeURL reads a shared redirect URL, preferring the mode-specific name
// (PAYMENT_TEST_SUCCESS_URL) over the plain one (PAYMENT_SUCCESS_URL).
func stripeModeURL(suffix string) string {
	modeSpecific := "PAYMENT_" + strings.ToUpper(stripeMode()) + "_" + suffix
	if v := strings.TrimSpace(os.Getenv(modeSpecific)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("PAYMENT_" + suffix))
}

// stripeSecretKey returns the API key for the current mode.
//
// A key whose prefix contradicts the mode is refused. This is the guard that
// stops a live key charging real cards during QA, and stops a test key
// silently failing every payment in production. PAYMENT_ALLOW_KEY_MISMATCH
// disables it for the rare deployment that genuinely needs the mismatch.
func stripeSecretKey() (string, error) {
	key := stripeEnv("SECRET_KEY")
	if key == "" {
		return "", errors.New("missing Stripe secret key: set STRIPE_SECRET_KEY (or the mode-specific STRIPE_TEST_SECRET_KEY / STRIPE_LIVE_SECRET_KEY)")
	}

	if strings.EqualFold(strings.TrimSpace(os.Getenv("PAYMENT_ALLOW_KEY_MISMATCH")), "true") {
		return key, nil
	}

	mode := stripeMode()
	switch {
	case strings.HasPrefix(key, "sk_live_"), strings.HasPrefix(key, "rk_live_"):
		if mode != "live" {
			return "", fmt.Errorf("a live Stripe key is configured but the function is in %s mode", mode)
		}
	case strings.HasPrefix(key, "sk_test_"), strings.HasPrefix(key, "rk_test_"):
		if mode != "test" {
			return "", fmt.Errorf("a test Stripe key is configured but the function is in %s mode", mode)
		}
	}
	return key, nil
}

// stripeClient builds an API client scoped to this request. It avoids
// stripe-go's package-level globals, which are not safe to mutate per request.
func stripeClient(key string) *client.API {
	newConfig := func() *stripe.BackendConfig {
		return &stripe.BackendConfig{
			HTTPClient:        &http.Client{Timeout: stripeTimeout},
			MaxNetworkRetries: stripe.Int64(1),
		}
	}

	apiConfig := newConfig()
	if stripeBackendURL != "" {
		apiConfig.URL = stripe.String(stripeBackendURL)
	}

	sc := &client.API{}
	sc.Init(key, &stripe.Backends{
		API:     stripe.GetBackendWithConfig(stripe.APIBackend, apiConfig),
		Connect: stripe.GetBackendWithConfig(stripe.ConnectBackend, newConfig()),
		Uploads: stripe.GetBackendWithConfig(stripe.UploadsBackend, newConfig()),
	})
	return sc
}

// ─────────────────────────────────────────────────────────────────────────────
// Responses
// ─────────────────────────────────────────────────────────────────────────────

// stripeFail returns a structured error with a real HTTP status code, so a
// client can branch on res.statusCode instead of digging through a 200 body.
func stripeFail(ctx openruntimes.Context, status int, code, message string) openruntimes.Response {
	return ctx.Res.Json(map[string]interface{}{
		"success": false,
		"gateway": "stripe",
		"mode":    stripeMode(),
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}, ctx.Res.WithStatusCode(status))
}

func stripeOK(ctx openruntimes.Context, data map[string]interface{}) openruntimes.Response {
	return ctx.Res.Json(map[string]interface{}{
		"success": true,
		"gateway": "stripe",
		"mode":    stripeMode(),
		"data":    data,
	})
}

// stripeSDKFailure turns a stripe-go error into the right HTTP status. A card
// decline is a 402, a bad parameter a 400, and an unreachable API a 502 —
// never a 200 with the failure hidden in the body.
func stripeSDKFailure(ctx openruntimes.Context, err error) openruntimes.Response {
	var serr *stripe.Error
	if errors.As(err, &serr) {
		status := serr.HTTPStatusCode
		if status == 0 {
			status = http.StatusBadGateway
		}
		code := string(serr.Code)
		if code == "" {
			code = string(serr.Type)
		}
		if code == "" {
			code = "stripe_error"
		}
		message := serr.Msg
		if message == "" {
			message = err.Error()
		}
		ctx.Error("Stripe rejected the request: " + message)
		return stripeFail(ctx, status, code, message)
	}

	ctx.Error("Stripe was unreachable: " + err.Error())
	return stripeFail(ctx, http.StatusBadGateway, "gateway_unreachable",
		"Could not reach Stripe: "+err.Error())
}

// ─────────────────────────────────────────────────────────────────────────────
// Routing
// ─────────────────────────────────────────────────────────────────────────────

// HandleStripe dispatches to the Stripe sub-routes.
//
//	POST /stripe/create   — open a Checkout Session, returns the hosted page URL
//	POST /stripe/verify   — ask Stripe whether a session was actually paid
//	POST /stripe/webhook  — receive Stripe's event notifications
func HandleStripe(ctx openruntimes.Context) openruntimes.Response {
	switch stripeSubRoute(ctx.Req.Path) {
	case "create":
		return stripeCreate(ctx)
	case "verify":
		return stripeVerify(ctx)
	case "webhook":
		return stripeWebhook(ctx)
	default:
		return stripeFail(ctx, http.StatusNotFound, "unknown_route",
			"Unknown Stripe route. Use /stripe/create, /stripe/verify or /stripe/webhook.")
	}
}

// stripeSubRoute extracts the segment after /stripe, ignoring a trailing slash.
func stripeSubRoute(path string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(path), "/")
	rest := strings.TrimPrefix(trimmed, "/stripe")
	return strings.ToLower(strings.TrimPrefix(rest, "/"))
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// appendQueryParam adds a query parameter to a URL that may already carry one.
//
// Naive concatenation produced "?ref=abc?session_id=..." for a success URL with
// an existing query string, which buried session_id inside the previous
// parameter's value. The value is inserted verbatim because Stripe's
// {CHECKOUT_SESSION_ID} placeholder must not be percent-encoded.
func appendQueryParam(rawURL, key, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		return "", fmt.Errorf("not an absolute URL: %q", rawURL)
	}

	param := key + "=" + value
	if u.RawQuery == "" {
		u.RawQuery = param
	} else {
		u.RawQuery += "&" + param
	}
	return u.String(), nil
}

// stripeFormatAmount renders minor units for display: 1500 USD is "15.00",
// 1500 JPY is "1500". Integer maths throughout — a float would lose precision
// on large amounts.
func stripeFormatAmount(minor int64, currency string) string {
	if stripeZeroDecimalCurrencies[strings.ToUpper(strings.TrimSpace(currency))] {
		return strconv.FormatInt(minor, 10)
	}

	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

// envAmount reads an amount limit from the environment, falling back to def.
func envAmount(name string, def int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return def
	}
	return v
}

// isCurrencyCode reports whether s is a three-letter ISO 4217 code.
func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// stripeOutcome maps a session's two status fields onto one answer, plus the
// only flag an order should ever be released on.
//
// "processing" is the subtle one: checkout finished but a delayed payment
// method has not settled and can still fail. Treating it as paid means
// shipping against a payment that may never arrive.
func stripeOutcome(s *stripe.CheckoutSession) (status string, paid bool) {
	switch string(s.PaymentStatus) {
	case "paid":
		return "paid", true
	case "no_payment_required":
		return "no_payment_required", true
	}

	switch string(s.Status) {
	case "expired":
		return "expired", false
	case "complete":
		return "processing", false
	default:
		return "pending", false
	}
}

// stripeOrderID recovers the merchant's order ID from a session.
func stripeOrderID(s *stripe.CheckoutSession) string {
	if s.ClientReferenceID != "" {
		return s.ClientReferenceID
	}
	return s.Metadata["order_id"]
}

// stripeSessionSummary is the shared payment view returned by verify and by
// the webhook, so both paths report an outcome identically.
func stripeSessionSummary(s *stripe.CheckoutSession) map[string]interface{} {
	status, paid := stripeOutcome(s)

	paymentIntentID := ""
	if s.PaymentIntent != nil {
		paymentIntentID = s.PaymentIntent.ID
	}

	customerEmail := s.CustomerEmail
	if s.CustomerDetails != nil && s.CustomerDetails.Email != "" {
		customerEmail = s.CustomerDetails.Email
	}

	currency := strings.ToUpper(string(s.Currency))

	return map[string]interface{}{
		"sessionId":       s.ID,
		"orderId":         stripeOrderID(s),
		"paid":            paid,
		"status":          status,
		"paymentStatus":   string(s.PaymentStatus),
		"sessionStatus":   string(s.Status),
		"amount":          s.AmountTotal,
		"amountDisplay":   stripeFormatAmount(s.AmountTotal, currency),
		"currency":        currency,
		"paymentIntentId": paymentIntentID,
		"customerEmail":   customerEmail,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /stripe/create
// ─────────────────────────────────────────────────────────────────────────────

type stripeCreatePayload struct {
	Amount        int64             `json:"amount"`
	Currency      string            `json:"currency"`
	OrderId       string            `json:"orderId"`
	ProductName   string            `json:"productName"`
	Description   string            `json:"description"`
	CustomerEmail string            `json:"customerEmail"`
	SuccessURL    string            `json:"successURL"`
	CancelURL     string            `json:"cancelURL"`
	Metadata      map[string]string `json:"metadata"`
}

// stripeCreate opens a Checkout Session and returns its hosted payment URL.
//
// Creating a session is not a payment. The order may only be marked paid once
// /stripe/verify or the webhook confirms Stripe actually charged the card.
func stripeCreate(ctx openruntimes.Context) openruntimes.Response {
	key, err := stripeSecretKey()
	if err != nil {
		ctx.Error("Stripe configuration error: " + err.Error())
		return stripeFail(ctx, http.StatusInternalServerError, "config_error", err.Error())
	}

	raw := ctx.Req.BodyRaw()
	if strings.TrimSpace(raw) == "" {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request", "Request body is empty.")
	}

	var p stripeCreatePayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request",
			"Request body is not valid JSON: "+err.Error())
	}

	orderID := strings.TrimSpace(p.OrderId)
	if orderID == "" {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request", "orderId is required.")
	}

	// currency is deliberately not defaulted. It used to fall back to "usd",
	// which turned a ৳1,500 order into a $15.00 charge whenever a client
	// forgot the field.
	currency := strings.TrimSpace(p.Currency)
	if currency == "" {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request",
			"currency is required (ISO 4217, e.g. \"USD\").")
	}
	if !isCurrencyCode(currency) {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("currency %q is not a three-letter ISO 4217 code.", currency))
	}
	currency = strings.ToUpper(currency)

	if p.Amount <= 0 {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request",
			"amount must be greater than 0, in minor units (e.g. 1500 = 15.00).")
	}
	if min := envAmount("STRIPE_MIN_AMOUNT", defaultMinAmount); p.Amount < min {
		return stripeFail(ctx, http.StatusBadRequest, "amount_too_small",
			fmt.Sprintf("amount %d is below the minimum of %d minor units.", p.Amount, min))
	}
	if max := envAmount("PAYMENT_MAX_AMOUNT", defaultMaxAmount); p.Amount > max {
		return stripeFail(ctx, http.StatusBadRequest, "amount_too_large",
			fmt.Sprintf("amount %d exceeds the maximum of %d minor units.", p.Amount, max))
	}

	// Per-request URLs let one deployment serve several apps with different
	// deep links; the environment supplies the default.
	successURL := strings.TrimSpace(p.SuccessURL)
	if successURL == "" {
		successURL = stripeModeURL("SUCCESS_URL")
	}
	cancelURL := strings.TrimSpace(p.CancelURL)
	if cancelURL == "" {
		cancelURL = stripeModeURL("CANCEL_URL")
	}
	if successURL == "" || cancelURL == "" {
		ctx.Error("Stripe configuration error: redirect URLs are not set")
		return stripeFail(ctx, http.StatusInternalServerError, "config_error",
			"Missing redirect URLs: set PAYMENT_SUCCESS_URL and PAYMENT_CANCEL_URL, or send successURL and cancelURL in the request.")
	}

	// Stripe substitutes the real session id for this placeholder on redirect,
	// which is what the client passes to /stripe/verify.
	successWithSession, err := appendQueryParam(successURL, "session_id", "{CHECKOUT_SESSION_ID}")
	if err != nil {
		return stripeFail(ctx, http.StatusInternalServerError, "config_error",
			"Invalid success URL: "+err.Error())
	}
	if _, err := appendQueryParam(cancelURL, "probe", "1"); err != nil {
		return stripeFail(ctx, http.StatusInternalServerError, "config_error",
			"Invalid cancel URL: "+err.Error())
	}

	productName := strings.TrimSpace(p.ProductName)
	if productName == "" {
		productName = "Order " + orderID
	}

	// The client's metadata is copied first so order_id cannot be spoofed
	// through it — reconciliation depends on that key being trustworthy.
	metadata := map[string]string{}
	for k, v := range p.Metadata {
		metadata[k] = v
	}
	metadata["order_id"] = orderID

	priceData := &stripe.CheckoutSessionLineItemPriceDataParams{
		Currency:   stripe.String(strings.ToLower(currency)),
		UnitAmount: stripe.Int64(p.Amount),
		ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
			Name: stripe.String(productName),
		},
	}
	if desc := strings.TrimSpace(p.Description); desc != "" {
		priceData.ProductData.Description = stripe.String(desc)
	}

	params := &stripe.CheckoutSessionParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL:        stripe.String(successWithSession),
		CancelURL:         stripe.String(cancelURL),
		ClientReferenceID: stripe.String(orderID),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{PriceData: priceData, Quantity: stripe.Int64(1)},
		},
		// The order ID must also land on the PaymentIntent: that is what the
		// dashboard's Payments list and every refund actually reference.
		PaymentIntentData: &stripe.CheckoutSessionPaymentIntentDataParams{
			Metadata: metadata,
		},
	}
	params.Metadata = metadata

	if email := strings.TrimSpace(p.CustomerEmail); email != "" {
		params.CustomerEmail = stripe.String(email)
	}

	// A retried create for the same order, amount and currency returns the
	// original session instead of opening a second one — safe for the network
	// retries a mobile client will inevitably make.
	params.IdempotencyKey = stripe.String(
		fmt.Sprintf("stripe:create:%s:%d:%s", orderID, p.Amount, currency))

	session, err := stripeClient(key).CheckoutSessions.New(params)
	if err != nil {
		return stripeSDKFailure(ctx, err)
	}

	ctx.Log("Stripe Checkout Session " + session.ID + " created for order " + orderID)

	return stripeOK(ctx, map[string]interface{}{
		"paymentURL":     session.URL,
		"sessionId":      session.ID,
		"orderId":        orderID,
		"amount":         p.Amount,
		"amountDisplay":  stripeFormatAmount(p.Amount, currency),
		"currency":       currency,
		"expiresAt":      session.ExpiresAt,
		"requiresVerify": true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /stripe/verify
// ─────────────────────────────────────────────────────────────────────────────

// stripeVerify asks Stripe whether a session was actually paid.
//
// Landing on the success URL proves nothing — anyone can open that URL, and a
// card can still be declined at the last moment. This is the call that decides
// whether an order may be released.
func stripeVerify(ctx openruntimes.Context) openruntimes.Response {
	key, err := stripeSecretKey()
	if err != nil {
		ctx.Error("Stripe configuration error: " + err.Error())
		return stripeFail(ctx, http.StatusInternalServerError, "config_error", err.Error())
	}

	raw := ctx.Req.BodyRaw()
	if strings.TrimSpace(raw) == "" {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request", "Request body is empty.")
	}

	var p struct {
		SessionID string `json:"sessionId"`
		OrderId   string `json:"orderId"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request",
			"Request body is not valid JSON: "+err.Error())
	}

	sessionID := strings.TrimSpace(p.SessionID)
	if sessionID == "" {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request", "sessionId is required.")
	}
	if !strings.HasPrefix(sessionID, "cs_") {
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request",
			"sessionId must be a Checkout Session id (it starts with \"cs_\").")
	}

	params := &stripe.CheckoutSessionParams{}
	params.AddExpand("payment_intent")

	session, err := stripeClient(key).CheckoutSessions.Get(sessionID, params)
	if err != nil {
		return stripeSDKFailure(ctx, err)
	}

	// Without this check a client could confirm someone else's paid session
	// against its own order.
	claimed := strings.TrimSpace(p.OrderId)
	if claimed != "" && claimed != stripeOrderID(session) {
		ctx.Error("Stripe session " + sessionID + " does not belong to order " + claimed)
		return stripeFail(ctx, http.StatusConflict, "order_mismatch",
			"This Checkout Session belongs to a different order.")
	}

	summary := stripeSessionSummary(session)
	ctx.Log(fmt.Sprintf("Stripe verify %s: status=%v paid=%v", sessionID, summary["status"], summary["paid"]))

	// 200 even when unpaid: the verification itself succeeded, and "paid"
	// carries the outcome.
	return stripeOK(ctx, summary)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /stripe/webhook
// ─────────────────────────────────────────────────────────────────────────────

// stripeHandledEvents are the Checkout events that carry a payment outcome.
var stripeHandledEvents = map[string]bool{
	"checkout.session.completed":               true,
	"checkout.session.async_payment_succeeded": true,
	"checkout.session.async_payment_failed":    true,
	"checkout.session.expired":                 true,
}

// stripeWebhook receives Stripe's event notifications.
//
// Stripe cannot send the function's x-api-secret header, so this route is
// exempt from that gate and authenticates with Stripe's HMAC signature instead.
func stripeWebhook(ctx openruntimes.Context) openruntimes.Response {
	secret := stripeEnv("WEBHOOK_SECRET")
	if secret == "" {
		ctx.Error("Stripe configuration error: webhook secret is not set")
		return stripeFail(ctx, http.StatusInternalServerError, "config_error",
			"Missing webhook secret: set STRIPE_WEBHOOK_SECRET (or the mode-specific STRIPE_TEST_WEBHOOK_SECRET / STRIPE_LIVE_WEBHOOK_SECRET).")
	}

	signature := ctx.Req.Headers["stripe-signature"]
	if strings.TrimSpace(signature) == "" {
		ctx.Error("Stripe webhook rejected: no signature header")
		return stripeFail(ctx, http.StatusBadRequest, "invalid_signature",
			"Missing Stripe-Signature header.")
	}

	// The signature authenticates the request; the account's API version is
	// set in the Stripe dashboard and routinely differs from the one stripe-go
	// pins. Rejecting on that difference would reject every genuine webhook.
	// The tolerance window is left at the default, which is what rejects a
	// captured event replayed later.
	event, err := webhook.ConstructEventWithOptions(
		[]byte(ctx.Req.BodyRaw()), signature, secret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true},
	)
	if err != nil {
		ctx.Error("Stripe webhook signature verification failed: " + err.Error())
		return stripeFail(ctx, http.StatusBadRequest, "invalid_signature",
			"Signature verification failed: "+err.Error())
	}

	// A live event arriving at a test deployment means the endpoint is
	// registered in the wrong Stripe dashboard. Acting on it would mix real
	// and test orders.
	if event.Livemode != (stripeMode() == "live") {
		ctx.Error("Stripe webhook rejected: livemode does not match " + stripeMode() + " mode")
		return stripeFail(ctx, http.StatusBadRequest, "mode_mismatch",
			fmt.Sprintf("Event livemode=%t reached a deployment running in %s mode.", event.Livemode, stripeMode()))
	}

	eventType := string(event.Type)
	data := map[string]interface{}{
		"received":  true,
		"handled":   false,
		"eventId":   event.ID,
		"eventType": eventType,
	}

	// Unknown types are acknowledged rather than refused: Stripe retries a
	// non-2xx for three days and eventually disables the endpoint.
	if !stripeHandledEvents[eventType] {
		ctx.Log("Stripe webhook: ignoring unhandled event type " + eventType)
		return stripeOK(ctx, data)
	}

	var session stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		ctx.Error("Stripe webhook: could not decode session from " + eventType + ": " + err.Error())
		return stripeFail(ctx, http.StatusBadRequest, "invalid_request",
			"Event payload did not contain a Checkout Session.")
	}

	summary := stripeSessionSummary(&session)

	// The event type is more specific than the session's own fields for these
	// two terminal outcomes.
	switch eventType {
	case "checkout.session.async_payment_failed":
		summary["status"] = "failed"
		summary["paid"] = false
	case "checkout.session.expired":
		summary["status"] = "expired"
		summary["paid"] = false
	}

	data["handled"] = true
	data["payment"] = summary

	ctx.Log(fmt.Sprintf("Stripe webhook %s: order=%v status=%v paid=%v",
		eventType, summary["orderId"], summary["status"], summary["paid"]))

	// ── Extension point ──────────────────────────────────────────────────
	// This function is stateless, so the event is verified, logged and
	// acknowledged but not persisted. Update your order store here.
	//
	// Two rules if you do:
	//   1. Be idempotent — Stripe redelivers events, so setting an
	//      already-paid order to paid must be a no-op.
	//   2. Stay fast and always answer 2xx — a slow or failing handler makes
	//      Stripe retry and eventually disable the endpoint.
	// ─────────────────────────────────────────────────────────────────────

	return stripeOK(ctx, data)
}
