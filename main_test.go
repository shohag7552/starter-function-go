package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
)

// newRequest builds a Context the way the Appwrite runtime would. Header keys
// arrive lower-cased from the runtime, which is why the lookups use that form.
func newRequest(t *testing.T, method, path, body string, headers map[string]string) openruntimes.Context {
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
	ctx.Req.Method = method
	ctx.Req.Path = path
	ctx.Req.SetBodyBinary([]byte(body))
	return ctx
}

func call(t *testing.T, method, path, body string, headers map[string]string) (int, map[string]interface{}) {
	t.Helper()

	res := Main(newRequest(t, method, path, body, headers))

	out := map[string]interface{}{}
	if len(res.Body) > 0 {
		if err := json.Unmarshal(res.Body, &out); err != nil {
			t.Fatalf("response is not JSON: %v (body=%s)", err, res.Body)
		}
	}

	status := res.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	return status, out
}

func codeOf(body map[string]interface{}) string {
	errObj, ok := body["error"].(map[string]interface{})
	if !ok {
		return ""
	}
	code, _ := errObj["code"].(string)
	return code
}

func withSecret(t *testing.T, secret string) {
	t.Helper()
	t.Setenv("API_SECRET", secret)
	t.Setenv("PAYMENT_MODE", "test")
	t.Setenv("STRIPE_MODE", "test")
	t.Setenv("ENABLE_DEBUG_ENDPOINT", "")
}

// ─────────────────────────────────────────────────────────────────────────────
// Authentication
// ─────────────────────────────────────────────────────────────────────────────

// The previous implementation skipped authentication entirely when API_SECRET
// was empty, so a deployment that lost its environment variables silently
// became a publicly writable payment API. It must fail closed instead.
func TestAuthFailsClosedWhenSecretMissing(t *testing.T) {
	withSecret(t, "")

	status, body := call(t, http.MethodPost, "/stripe/create", `{}`, nil)

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%v)", status, body)
	}
	if got := codeOf(body); got != "config_error" {
		t.Errorf("error code = %q, want config_error", got)
	}
}

func TestAuthRejectsMissingHeader(t *testing.T) {
	withSecret(t, "supersecret")

	status, body := call(t, http.MethodPost, "/stripe/create", `{}`, nil)

	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	if got := codeOf(body); got != "unauthorized" {
		t.Errorf("error code = %q, want unauthorized", got)
	}
}

func TestAuthRejectsWrongSecret(t *testing.T) {
	withSecret(t, "supersecret")

	status, _ := call(t, http.MethodPost, "/stripe/create", `{}`,
		map[string]string{"x-api-secret": "wrong"})

	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
}

// A correct secret must get past the gate. Reaching the Stripe handler and
// failing there on a validation error proves authentication succeeded.
func TestAuthAcceptsCorrectSecret(t *testing.T) {
	withSecret(t, "supersecret")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_dummy")
	t.Setenv("PAYMENT_SUCCESS_URL", "https://app.example.com/success")
	t.Setenv("PAYMENT_CANCEL_URL", "https://app.example.com/cancel")

	status, body := call(t, http.MethodPost, "/stripe/create", `{}`,
		map[string]string{"x-api-secret": "supersecret"})

	if status == http.StatusUnauthorized {
		t.Fatalf("authorised request was rejected: %v", body)
	}
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 from request validation (body=%v)", status, body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhook exemption
// ─────────────────────────────────────────────────────────────────────────────

// Stripe cannot send our custom header, so webhook routes must bypass the
// shared-secret gate and authenticate by signature instead. Reaching the
// handler's own signature check proves the bypass works.
func TestWebhookRoutesBypassAPISecret(t *testing.T) {
	withSecret(t, "supersecret")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")

	status, body := call(t, http.MethodPost, "/stripe/webhook", `{"id":"evt_1"}`, nil)

	if status == http.StatusUnauthorized {
		t.Fatalf("webhook was blocked by the API secret gate: %v", body)
	}
	if got := codeOf(body); got != "invalid_signature" {
		t.Errorf("error code = %q, want invalid_signature (body=%v)", got, body)
	}
}

// The bypass must not become a way to reach a paid endpoint unauthenticated.
func TestNonWebhookRoutesStillRequireSecret(t *testing.T) {
	withSecret(t, "supersecret")

	for _, path := range []string{
		"/stripe/create",
		"/stripe/verify",
		"/debug",
		"/nope",
	} {
		t.Run(path, func(t *testing.T) {
			status, _ := call(t, http.MethodPost, path, `{}`, nil)
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 for unauthenticated %s", status, path)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Method and routing
// ─────────────────────────────────────────────────────────────────────────────

// The method check runs before authentication so a wrong method gets a clear
// 405 rather than a misleading 401.
func TestMethodNotAllowed(t *testing.T) {
	withSecret(t, "supersecret")

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			status, body := call(t, method, "/stripe/create", ``, nil)
			if status != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", status)
			}
			if got := codeOf(body); got != "method_not_allowed" {
				t.Errorf("error code = %q, want method_not_allowed", got)
			}
		})
	}
}

// The old strings.HasPrefix routing sent "/stripe-anything-at-all" to the
// Stripe handler. Only the gateway segment itself, or a path under it, should
// match.
func TestRoutePrefixIsNotGreedy(t *testing.T) {
	withSecret(t, "supersecret")
	auth := map[string]string{"x-api-secret": "supersecret"}

	for _, path := range []string{
		"/stripe-anything-at-all",
		"/stripefoo",
		"/stripe.create",
		"/stripex/create",
	} {
		t.Run(path, func(t *testing.T) {
			status, body := call(t, http.MethodPost, path, `{}`, auth)
			if status != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (body=%v)", status, body)
			}
		})
	}
}

func TestGatewayRoutesMatch(t *testing.T) {
	withSecret(t, "supersecret")
	auth := map[string]string{"x-api-secret": "supersecret"}

	// These reach their handler and fail on configuration or validation, not
	// on routing — any status other than 404 proves the route matched.
	for _, path := range []string{
		"/stripe/create",
		"/stripe/verify",
		"/stripe/webhook",
	} {
		t.Run(path, func(t *testing.T) {
			status, body := call(t, http.MethodPost, path, `{}`, auth)
			if status == http.StatusNotFound {
				t.Errorf("route %s was not matched (body=%v)", path, body)
			}
		})
	}
}

func TestTrailingSlashRoutesIdentically(t *testing.T) {
	withSecret(t, "supersecret")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_dummy")
	t.Setenv("PAYMENT_SUCCESS_URL", "https://app.example.com/success")
	t.Setenv("PAYMENT_CANCEL_URL", "https://app.example.com/cancel")
	auth := map[string]string{"x-api-secret": "supersecret"}

	withSlash, _ := call(t, http.MethodPost, "/stripe/create/", `{}`, auth)
	without, _ := call(t, http.MethodPost, "/stripe/create", `{}`, auth)

	if withSlash != without {
		t.Errorf("trailing slash changed the outcome: %d vs %d", withSlash, without)
	}
}

func TestUnknownRouteListsEndpoints(t *testing.T) {
	withSecret(t, "supersecret")

	status, body := call(t, http.MethodPost, "/nope", `{}`,
		map[string]string{"x-api-secret": "supersecret"})

	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if _, ok := body["endpoints"]; !ok {
		t.Errorf("404 response should list the available endpoints: %v", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Debug endpoint
// ─────────────────────────────────────────────────────────────────────────────

// /debug reports configuration state, so it stays off unless explicitly enabled.
func TestDebugEndpointDisabledByDefault(t *testing.T) {
	withSecret(t, "supersecret")

	status, body := call(t, http.MethodPost, "/debug", `{}`,
		map[string]string{"x-api-secret": "supersecret"})

	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%v)", status, body)
	}
}

func TestDebugEndpointWhenEnabled(t *testing.T) {
	withSecret(t, "supersecret")
	t.Setenv("ENABLE_DEBUG_ENDPOINT", "true")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_supersecretvalue")

	status, body := call(t, http.MethodPost, "/debug", `{}`,
		map[string]string{"x-api-secret": "supersecret"})

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%v)", status, body)
	}

	vars, ok := body["vars"].(map[string]interface{})
	if !ok {
		t.Fatalf("no vars object: %v", body)
	}
	if vars["STRIPE_SECRET_KEY"] != "SET" {
		t.Errorf("STRIPE_SECRET_KEY = %v, want SET", vars["STRIPE_SECRET_KEY"])
	}

	// The old version leaked the first four characters of every secret, which
	// distinguished sk_live from sk_test. Only set/not-set is reported now.
	raw := string(mustMarshal(t, body))
	if contains(raw, "supersecretvalue") || contains(raw, "sk_t") {
		t.Errorf("debug output leaked part of a secret: %s", raw)
	}
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	return b
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
