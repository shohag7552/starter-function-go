package gateways

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	razorpay "github.com/razorpay/razorpay-go"
)

// HandleRazorpay creates a Razorpay order.
// Endpoint: POST /razorpay/create
// Env vars: RAZORPAY_KEY_ID, RAZORPAY_KEY_SECRET
func HandleRazorpay(ctx openruntimes.Context) openruntimes.Response {
	keyID := os.Getenv("RAZORPAY_KEY_ID")
	keySecret := os.Getenv("RAZORPAY_KEY_SECRET")

	if keyID == "" || keySecret == "" {
		ctx.Error("❌ Razorpay credentials missing")
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Server config error: missing Razorpay credentials",
		})
	}

	if ctx.Req.BodyRaw() == "" {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Request body is empty"})
	}

	type Payload struct {
		Amount   int64  `json:"amount"`   // Amount in paise (e.g., 50000 = ₹500)
		Currency string `json:"currency"` // e.g., "INR"
		OrderId  string `json:"orderId"`  // Your receipt/order ID
	}
	var p Payload
	if err := json.Unmarshal([]byte(ctx.Req.BodyRaw()), &p); err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Invalid request format"})
	}
	if p.Amount <= 0 || p.OrderId == "" {
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "amount and orderId are required",
		})
	}
	if p.Currency == "" {
		p.Currency = "INR"
	}

	// Create Razorpay client
	client := razorpay.NewClient(keyID, keySecret)

	// Create order
	orderData := map[string]interface{}{
		"amount":   p.Amount,
		"currency": p.Currency,
		"receipt":  p.OrderId,
	}

	order, err := client.Order.Create(orderData, nil)
	if err != nil {
		ctx.Error("❌ Razorpay API Error: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Failed to create Razorpay order: " + err.Error(),
		})
	}

	razorpayOrderID := fmt.Sprintf("%v", order["id"])
	ctx.Log("✅ Razorpay order created: " + razorpayOrderID)

	return ctx.Res.Json(map[string]interface{}{
		"success": true, "gateway": "razorpay",
		"data": map[string]interface{}{
			"orderId": razorpayOrderID,
			"keyId":   keyID,
			"amount":  p.Amount,
			"currency": p.Currency,
		},
	})
}
