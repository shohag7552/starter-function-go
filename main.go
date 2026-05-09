package handler

import (
	"os"
	"strings"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	"openruntimes/handler/gateways"
)

// Main is the entry point for the Appwrite Cloud Function.
// It routes requests to the appropriate payment gateway handler based on the URL path.
//
// Supported routes:
//   POST /stripe/create      → Stripe Checkout Session
//   POST /sslcommerz/create  → SSLCommerz session
//   POST /bkash/create       → bKash create payment
//   POST /bkash/execute      → bKash execute payment
//   POST /razorpay/create    → Razorpay Payment Link
//   POST /paypal/create      → PayPal order
//
// Security:
//   - Requires API_SECRET header to match the API_SECRET environment variable
//   - Only POST method is allowed
func Main(Context openruntimes.Context) openruntimes.Response {
	path := Context.Req.Path
	method := Context.Req.Method

	Context.Log("📍 Request received: " + method + " " + path)

	// ─── Security: Verify API Secret ───
	// Prevents unauthorized access. Set API_SECRET env var and send it
	// as "x-api-secret" header from your Flutter app.
	apiSecret := os.Getenv("API_SECRET")
	if apiSecret != "" {
		requestSecret := Context.Req.Headers["x-api-secret"]
		if requestSecret != apiSecret {
			Context.Error("❌ Unauthorized request — invalid or missing API secret")
			return Context.Res.Json(map[string]interface{}{
				"success": false,
				"error":   "Unauthorized",
			}, Context.Res.WithStatusCode(401))
		}
	}

	// ─── Only allow POST requests ───
	if method != "" && strings.ToUpper(method) != "POST" {
		return Context.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Method not allowed. Use POST.",
		}, Context.Res.WithStatusCode(405))
	}

	// ─── Route to the correct gateway handler ───
	switch {
	case strings.HasPrefix(path, "/stripe"):
		return gateways.HandleStripe(Context)

	case strings.HasPrefix(path, "/sslcommerz"):
		return gateways.HandleSSLCommerz(Context)

	case strings.HasPrefix(path, "/bkash"):
		return gateways.HandleBkash(Context)

	case strings.HasPrefix(path, "/razorpay"):
		return gateways.HandleRazorpay(Context)

	case strings.HasPrefix(path, "/paypal"):
		return gateways.HandlePaypal(Context)

	default:
		return Context.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Unknown gateway. Use one of the supported endpoints.",
			"endpoints": map[string]string{
				"stripe":     "POST /stripe/create",
				"sslcommerz": "POST /sslcommerz/create",
				"bkash":      "POST /bkash/create or /bkash/execute",
				"razorpay":   "POST /razorpay/create",
				"paypal":     "POST /paypal/create",
			},
		})
	}
}