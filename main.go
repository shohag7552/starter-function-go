package handler

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	"openruntimes/handler/gateways"
)

// Main is the entry point for the Appwrite Cloud Function.
//
// Routes:
//
//	POST /stripe/create   → open a Stripe Checkout Session, returns the hosted page URL
//	POST /stripe/verify   → ask Stripe whether a session was actually paid
//	POST /stripe/webhook  → receive Stripe's event notifications
//	POST /debug           → configuration report (only when ENABLE_DEBUG_ENDPOINT=true)
//
// Every route except the webhook requires the x-api-secret header to match the
// API_SECRET environment variable. The webhook cannot carry a custom header, so
// it authenticates with Stripe's HMAC signature inside the handler instead.
func Main(Context openruntimes.Context) openruntimes.Response {
	path := normalizePath(Context.Req.Path)
	method := strings.ToUpper(strings.TrimSpace(Context.Req.Method))

	Context.Log("Request received: " + method + " " + path)

	// ─── Method check ───
	// Runs before authentication so a wrong method gets a clear 405 rather
	// than a misleading 401.
	if method != "" && method != http.MethodPost {
		return fail(Context, http.StatusMethodNotAllowed, "method_not_allowed",
			"Method not allowed. Use POST.")
	}

	// ─── Authentication ───
	if !isWebhookRoute(path) {
		// Fails closed. The previous version skipped this check entirely when
		// API_SECRET was empty, so a deployment that lost its environment
		// variables silently became a publicly writable payment API.
		apiSecret := os.Getenv("API_SECRET")
		if apiSecret == "" {
			Context.Error("API_SECRET is not set — refusing every request")
			return fail(Context, http.StatusInternalServerError, "config_error",
				"Server configuration error: API_SECRET is not set.")
		}

		given := Context.Req.Headers["x-api-secret"]
		if subtle.ConstantTimeCompare([]byte(given), []byte(apiSecret)) != 1 {
			Context.Error("Unauthorized request: invalid or missing API secret")
			return fail(Context, http.StatusUnauthorized, "unauthorized",
				"Invalid or missing x-api-secret header.")
		}
	}

	// ─── Routing ───
	// Matches on the whole first segment. Prefix matching used to send
	// "/stripe-anything-at-all" to the Stripe handler.
	switch firstSegment(path) {
	case "stripe":
		return gateways.HandleStripe(Context)

	case "debug":
		// Off unless explicitly enabled: it reports configuration state.
		if !strings.EqualFold(strings.TrimSpace(os.Getenv("ENABLE_DEBUG_ENDPOINT")), "true") {
			return notFound(Context)
		}
		return debugReport(Context)

	default:
		return notFound(Context)
	}
}

// normalizePath trims whitespace and a trailing slash so "/stripe/create/" and
// "/stripe/create" route identically.
func normalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "/"
	}
	if len(trimmed) > 1 {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	return trimmed
}

// firstSegment returns the first path segment, lower-cased. "/stripe/create"
// gives "stripe".
func firstSegment(path string) string {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)
	return strings.ToLower(parts[0])
}

func isWebhookRoute(path string) bool {
	return strings.EqualFold(path, "/stripe/webhook")
}

func fail(ctx openruntimes.Context, status int, code, message string) openruntimes.Response {
	return ctx.Res.Json(map[string]interface{}{
		"success": false,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}, ctx.Res.WithStatusCode(status))
}

func notFound(ctx openruntimes.Context) openruntimes.Response {
	return ctx.Res.Json(map[string]interface{}{
		"success": false,
		"error": map[string]interface{}{
			"code":    "unknown_route",
			"message": "Unknown route.",
		},
		"endpoints": map[string]string{
			"create":  "POST /stripe/create",
			"verify":  "POST /stripe/verify",
			"webhook": "POST /stripe/webhook",
		},
	}, ctx.Res.WithStatusCode(http.StatusNotFound))
}

// debugReport says only whether each variable is set. The previous version
// returned the first four characters of every secret, which was enough to tell
// an sk_live key from an sk_test one.
func debugReport(ctx openruntimes.Context) openruntimes.Response {
	isSet := func(key string) string {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return "NOT SET"
		}
		return "SET"
	}

	vars := map[string]string{}
	for _, key := range []string{
		"API_SECRET",
		"PAYMENT_MODE",
		"STRIPE_MODE",
		"STRIPE_SECRET_KEY",
		"STRIPE_TEST_SECRET_KEY",
		"STRIPE_LIVE_SECRET_KEY",
		"STRIPE_WEBHOOK_SECRET",
		"STRIPE_TEST_WEBHOOK_SECRET",
		"STRIPE_LIVE_WEBHOOK_SECRET",
		"PAYMENT_SUCCESS_URL",
		"PAYMENT_CANCEL_URL",
		"PAYMENT_TEST_SUCCESS_URL",
		"PAYMENT_TEST_CANCEL_URL",
		"STRIPE_MIN_AMOUNT",
		"PAYMENT_MAX_AMOUNT",
		"PAYMENT_ALLOW_KEY_MISMATCH",
	} {
		vars[key] = isSet(key)
	}

	return ctx.Res.Json(map[string]interface{}{
		"success": true,
		"message": "Environment variable status",
		"vars":    vars,
	})
}
