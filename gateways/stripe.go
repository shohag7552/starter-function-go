package gateways

import (
	"encoding/json"
	"os"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentintent"
)

// HandleStripe creates a Stripe PaymentIntent and returns the clientSecret.
//
// Endpoint: POST /stripe/create
//
// Request body:
//
//	{
//	  "amount": 1500,        // Amount in cents (e.g., 1500 = $15.00)
//	  "currency": "usd",     // Optional, defaults to "usd"
//	  "orderId": "order_123"
//	}
//
// Response:
//
//	{
//	  "success": true,
//	  "gateway": "stripe",
//	  "data": {
//	    "clientSecret": "pi_xxx_secret_xxx",
//	    "paymentIntentId": "pi_xxx"
//	  }
//	}
//
// Environment variables:
//   - STRIPE_SECRET_KEY (required)
func HandleStripe(Context openruntimes.Context) openruntimes.Response {
	// 1. Retrieve the Stripe Secret Key from environment
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")
	if stripe.Key == "" {
		Context.Error("❌ STRIPE_SECRET_KEY is missing from environment variables")
		return Context.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Server configuration error: missing Stripe key",
		})
	}

	// 2. Parse the request body
	if Context.Req.BodyRaw() == "" {
		return Context.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Request body is empty",
		})
	}

	type StripePayload struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		OrderId  string `json:"orderId"`
	}

	var payload StripePayload
	err := json.Unmarshal([]byte(Context.Req.BodyRaw()), &payload)
	if err != nil {
		Context.Error("❌ Failed to parse JSON: " + err.Error())
		return Context.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Invalid request format",
		})
	}

	// Validate required fields
	if payload.Amount <= 0 {
		return Context.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Amount must be greater than 0",
		})
	}

	if payload.OrderId == "" {
		return Context.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "orderId is required",
		})
	}

	// Set default currency
	if payload.Currency == "" {
		payload.Currency = "usd"
	}

	// 3. Create the PaymentIntent
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(payload.Amount),
		Currency: stripe.String(payload.Currency),
		Metadata: map[string]string{
			"order_id": payload.OrderId,
		},
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		Context.Error("❌ Stripe API Error: " + err.Error())
		return Context.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Failed to create payment intent: " + err.Error(),
		})
	}

	Context.Log("✅ Stripe PaymentIntent created for Order: " + payload.OrderId)

	// 4. Return the client secret
	return Context.Res.Json(map[string]interface{}{
		"success": true,
		"gateway": "stripe",
		"data": map[string]interface{}{
			"clientSecret":    pi.ClientSecret,
			"paymentIntentId": pi.ID,
		},
	})
}
