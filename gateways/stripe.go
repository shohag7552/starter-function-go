package gateways

import (
	"encoding/json"
	"os"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/checkout/session"
)

// HandleStripe creates a Stripe Checkout Session and returns a hosted payment page URL.
// The user completes payment on Stripe's hosted page, then gets redirected to success/fail URL.
//
// Endpoint: POST /stripe/create
//
// Request body:
//
//	{
//	  "amount": 1500,           // Amount in cents (e.g., 1500 = $15.00)
//	  "currency": "usd",       // Optional, defaults to "usd"
//	  "orderId": "order_123",
//	  "productName": "My Order" // Optional, defaults to "Order {orderId}"
//	}
//
// Response:
//
//	{
//	  "success": true,
//	  "gateway": "stripe",
//	  "data": {
//	    "paymentURL": "https://checkout.stripe.com/c/pay/cs_xxx",
//	    "sessionId": "cs_xxx"
//	  }
//	}
//
// Environment variables:
//   - STRIPE_SECRET_KEY (required)
//   - PAYMENT_SUCCESS_URL (required) — Stripe appends ?session_id={CHECKOUT_SESSION_ID}
//   - PAYMENT_CANCEL_URL (required)
func HandleStripe(ctx openruntimes.Context) openruntimes.Response {
	// 1. Retrieve environment variables
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")
	successURL := os.Getenv("PAYMENT_SUCCESS_URL")
	cancelURL := os.Getenv("PAYMENT_CANCEL_URL")

	if stripe.Key == "" {
		ctx.Error("❌ STRIPE_SECRET_KEY is missing from environment variables")
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Server configuration error: missing Stripe key",
		})
	}

	if successURL == "" || cancelURL == "" {
		ctx.Error("❌ PAYMENT_SUCCESS_URL or PAYMENT_CANCEL_URL is missing")
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Server configuration error: missing callback URLs",
		})
	}

	// 2. Parse the request body
	if ctx.Req.BodyRaw() == "" {
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Request body is empty",
		})
	}

	type StripePayload struct {
		Amount      int64  `json:"amount"`
		Currency    string `json:"currency"`
		OrderId     string `json:"orderId"`
		ProductName string `json:"productName"`
	}

	var payload StripePayload
	if err := json.Unmarshal([]byte(ctx.Req.BodyRaw()), &payload); err != nil {
		ctx.Error("❌ Failed to parse JSON: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Invalid request format",
		})
	}

	// Validate required fields
	if payload.Amount <= 0 {
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Amount must be greater than 0",
		})
	}

	if payload.OrderId == "" {
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "orderId is required",
		})
	}

	// Set defaults
	if payload.Currency == "" {
		payload.Currency = "usd"
	}
	if payload.ProductName == "" {
		payload.ProductName = "Order " + payload.OrderId
	}

	// 3. Create Checkout Session (hosted payment page)
	// Append session_id to success URL so Flutter can verify the payment
	stripeSuccessURL := successURL + "?session_id={CHECKOUT_SESSION_ID}"

	params := &stripe.CheckoutSessionParams{
		Mode: stripe.String(string(stripe.CheckoutSessionModePayment)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency:   stripe.String(payload.Currency),
					UnitAmount: stripe.Int64(payload.Amount),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name: stripe.String(payload.ProductName),
					},
				},
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: stripe.String(stripeSuccessURL),
		CancelURL:  stripe.String(cancelURL),
		Metadata: map[string]string{
			"order_id": payload.OrderId,
		},
	}

	s, err := session.New(params)
	if err != nil {
		ctx.Error("❌ Stripe Checkout Session Error: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Failed to create checkout session: " + err.Error(),
		})
	}

	ctx.Log("✅ Stripe Checkout Session created for Order: " + payload.OrderId)

	// 4. Return the hosted payment page URL
	return ctx.Res.Json(map[string]interface{}{
		"success": true,
		"gateway": "stripe",
		"data": map[string]interface{}{
			"paymentURL": s.URL,
			"sessionId":  s.ID,
		},
	})
}
