package gateways

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	razorpay "github.com/razorpay/razorpay-go"
)

// HandleRazorpay creates a Razorpay Payment Link and returns a hosted payment page URL.
// The user completes payment on Razorpay's hosted page, then gets redirected to the callback URL.
//
// Endpoint: POST /razorpay/create
//
// Request body:
//
//	{
//	  "amount": 50000,          // Amount in paise (e.g., 50000 = ₹500.00)
//	  "currency": "INR",       // Optional, defaults to "INR"
//	  "orderId": "order_123",
//	  "customerName": "John",  // Optional
//	  "customerEmail": "j@x.com", // Optional
//	  "customerPhone": "9999999999" // Optional
//	}
//
// Response:
//
//	{
//	  "success": true,
//	  "gateway": "razorpay",
//	  "data": {
//	    "paymentURL": "https://rzp.io/i/xxx",
//	    "paymentLinkId": "plink_xxx"
//	  }
//	}
//
// Environment variables:
//   - RAZORPAY_KEY_ID (required)
//   - RAZORPAY_KEY_SECRET (required)
//   - PAYMENT_SUCCESS_URL (required) — used as callback_url after payment
func HandleRazorpay(ctx openruntimes.Context) openruntimes.Response {
	keyID := os.Getenv("RAZORPAY_KEY_ID")
	keySecret := os.Getenv("RAZORPAY_KEY_SECRET")
	callbackURL := os.Getenv("PAYMENT_SUCCESS_URL")

	if keyID == "" || keySecret == "" {
		ctx.Error("❌ Razorpay credentials missing")
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Server config error: missing Razorpay credentials",
		})
	}

	if callbackURL == "" {
		ctx.Error("❌ PAYMENT_SUCCESS_URL is missing")
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Server config error: missing callback URL",
		})
	}

	if ctx.Req.BodyRaw() == "" {
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Request body is empty",
		})
	}

	type Payload struct {
		Amount        int64  `json:"amount"`
		Currency      string `json:"currency"`
		OrderId       string `json:"orderId"`
		CustomerName  string `json:"customerName"`
		CustomerEmail string `json:"customerEmail"`
		CustomerPhone string `json:"customerPhone"`
	}

	var p Payload
	if err := json.Unmarshal([]byte(ctx.Req.BodyRaw()), &p); err != nil {
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Invalid request format",
		})
	}

	if p.Amount <= 0 || p.OrderId == "" {
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "amount and orderId are required",
		})
	}

	if p.Currency == "" {
		p.Currency = "INR"
	}

	// Create Razorpay client
	client := razorpay.NewClient(keyID, keySecret)

	// Build Payment Link data
	linkData := map[string]interface{}{
		"amount":          p.Amount,
		"currency":        p.Currency,
		"accept_partial":  false,
		"description":     "Order " + p.OrderId,
		"reference_id":    p.OrderId,
		"callback_url":    callbackURL,
		"callback_method": "get",
		"notify": map[string]interface{}{
			"sms":   false,
			"email": false,
		},
		"reminder_enable": false,
	}

	// Add customer info if provided
	customer := map[string]interface{}{}
	if p.CustomerName != "" {
		customer["name"] = p.CustomerName
	}
	if p.CustomerEmail != "" {
		customer["email"] = p.CustomerEmail
	}
	if p.CustomerPhone != "" {
		customer["contact"] = p.CustomerPhone
	}
	if len(customer) > 0 {
		linkData["customer"] = customer
	}

	// Create Payment Link
	link, err := client.PaymentLink.Create(linkData, nil)
	if err != nil {
		ctx.Error("❌ Razorpay Payment Link Error: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{
			"success": false,
			"error":   "Failed to create Razorpay payment link: " + err.Error(),
		})
	}

	shortURL := fmt.Sprintf("%v", link["short_url"])
	linkID := fmt.Sprintf("%v", link["id"])

	ctx.Log("✅ Razorpay Payment Link created: " + linkID)

	return ctx.Res.Json(map[string]interface{}{
		"success": true,
		"gateway": "razorpay",
		"data": map[string]interface{}{
			"paymentURL":    shortURL,
			"paymentLinkId": linkID,
		},
	})
}
