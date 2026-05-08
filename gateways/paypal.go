package gateways

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
	"github.com/plutov/paypal/v4"
)

// HandlePaypal creates a PayPal order and returns the approval URL.
// Endpoint: POST /paypal/create
// Env vars: PAYPAL_CLIENT_ID, PAYPAL_CLIENT_SECRET, PAYPAL_IS_SANDBOX,
//           PAYMENT_SUCCESS_URL, PAYMENT_CANCEL_URL
func HandlePaypal(ctx openruntimes.Context) openruntimes.Response {
	clientID := os.Getenv("PAYPAL_CLIENT_ID")
	clientSecret := os.Getenv("PAYPAL_CLIENT_SECRET")
	isSandbox := os.Getenv("PAYPAL_IS_SANDBOX")
	successURL := os.Getenv("PAYMENT_SUCCESS_URL")
	cancelURL := os.Getenv("PAYMENT_CANCEL_URL")

	if clientID == "" || clientSecret == "" {
		ctx.Error("❌ PayPal credentials missing")
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Server config error: missing PayPal credentials",
		})
	}

	if ctx.Req.BodyRaw() == "" {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Request body is empty"})
	}

	type Payload struct {
		Amount   int64  `json:"amount"`   // Amount in cents
		Currency string `json:"currency"` // e.g., "USD"
		OrderId  string `json:"orderId"`
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
		p.Currency = "USD"
	}

	// Determine API base
	apiBase := paypal.APIBaseLive
	if isSandbox == "" || isSandbox == "true" {
		apiBase = paypal.APIBaseSandBox
	}

	// Create PayPal client
	ppClient, err := paypal.NewClient(clientID, clientSecret, apiBase)
	if err != nil {
		ctx.Error("❌ Failed to create PayPal client: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Failed to initialize PayPal",
		})
	}

	// Get access token
	_, err = ppClient.GetAccessToken(context.Background())
	if err != nil {
		ctx.Error("❌ PayPal auth failed: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "PayPal authentication failed",
		})
	}

	// Format amount: convert cents to dollars string (e.g., 1500 -> "15.00")
	amtStr := fmt.Sprintf("%.2f", float64(p.Amount)/100.0)

	// Build application context with return/cancel URLs for WebView redirect
	var appCtx *paypal.ApplicationContext
	if successURL != "" && cancelURL != "" {
		appCtx = &paypal.ApplicationContext{
			ReturnURL: successURL,
			CancelURL: cancelURL,
		}
	}

	// Create order
	order, err := ppClient.CreateOrder(
		context.Background(),
		paypal.OrderIntentCapture,
		[]paypal.PurchaseUnitRequest{
			{
				ReferenceID: p.OrderId,
				Amount: &paypal.PurchaseUnitAmount{
					Currency: p.Currency,
					Value:    amtStr,
				},
				Description: "Order " + p.OrderId,
			},
		},
		nil,
		appCtx,
	)
	if err != nil {
		ctx.Error("❌ PayPal create order failed: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Failed to create PayPal order: " + err.Error(),
		})
	}

	// Find approval URL from links
	approveURL := ""
	for _, link := range order.Links {
		if link.Rel == "approve" {
			approveURL = link.Href
			break
		}
	}

	ctx.Log("✅ PayPal order created: " + order.ID)

	return ctx.Res.Json(map[string]interface{}{
		"success": true, "gateway": "paypal",
		"data": map[string]interface{}{
			"paymentURL":    approveURL,
			"paypalOrderId": order.ID,
			"approveURL":    approveURL,
			"status":        order.Status,
		},
	})
}
