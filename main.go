package handler

import (
	"encoding/json"
	"os"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentintent"
)

// Define the incoming data from Flutter
type Payload struct {
	Amount   int64  `json:"amount"` // Amount in cents (e.g., 1500 = $15.00)
	Currency string `json:"currency"`
	OrderId  string `json:"orderId"`
}

func Main(Context openruntimes.Context) openruntimes.Response {
	// 1. Retrieve the secure Stripe Secret Key from Appwrite Environment Variables
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	if stripe.Key == "" {
		Context.Error("❌ STRIPE_SECRET_KEY is missing from environment variables")
		return Context.Res.Json(map[string]interface{}{
			"error": "Server configuration error",
		})
	}

	// 2. Parse the JSON body sent from Flutter
	if Context.Req.BodyRaw() == "" {
		return Context.Res.Json(map[string]interface{}{
			"error": "Request body is empty",
		})
	}

	var payload Payload
	err := json.Unmarshal([]byte(Context.Req.BodyRaw()), &payload)
	if err != nil {
		Context.Error("❌ Failed to parse JSON: " + err.Error())
		return Context.Res.Json(map[string]interface{}{
			"error": "Invalid request format",
		})
	}

	// Set a default currency if none is provided
	if payload.Currency == "" {
		payload.Currency = "usd"
	}

	// 3. Create the PaymentIntent with Stripe
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(payload.Amount),
		Currency: stripe.String(payload.Currency),
		// Adding metadata helps you track which order this payment belongs to in your Stripe Dashboard
		Metadata: map[string]string{
			"order_id": payload.OrderId,
		},
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		Context.Error("❌ Stripe API Error: " + err.Error())
		return Context.Res.Json(map[string]interface{}{
			"error": err.Error(),
		})
	}

	Context.Log("✅ Payment Intent created successfully for Order: " + payload.OrderId)

	// 4. Return the Client Secret back to the Flutter app
	return Context.Res.Json(map[string]interface{}{
		"clientSecret": pi.ClientSecret,
	})
}