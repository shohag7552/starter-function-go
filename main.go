package handler

import (
	"strings"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	"openruntimes/handler/gateways"
)

// Main is the entry point for the Appwrite Cloud Function.
// It routes requests to the appropriate payment gateway handler based on the URL path.
//
// Supported routes:
//   POST /stripe/create      → Stripe PaymentIntent
//   POST /sslcommerz/create  → SSLCommerz session
//   POST /bkash/create       → bKash create payment
//   POST /bkash/execute      → bKash execute payment
//   POST /razorpay/create    → Razorpay order
//   POST /paypal/create      → PayPal order
func Main(Context openruntimes.Context) openruntimes.Response {
	path := Context.Req.Path

	Context.Log("📍 Request received: " + Context.Req.Method + " " + path)

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