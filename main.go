package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
)

type Payload struct {
	Amount   string `json:"amount"` // e.g., "15.00"
	Currency string `json:"currency"`
}

func Main(Context openruntimes.Context) openruntimes.Response {
	clientID := os.Getenv("PAYPAL_CLIENT_ID")
	secret := os.Getenv("PAYPAL_SECRET")
	environment := os.Getenv("PAYPAL_ENVIRONMENT") // "sandbox" or "live"

	if clientID == "" || secret == "" {
		Context.Error("❌ PayPal credentials missing from environment variables")
		return Context.Res.Json(map[string]interface{}{"error": "Server configuration error"})
	}

	baseURL := "https://api-m.sandbox.paypal.com"
	if environment == "live" {
		baseURL = "https://api-m.paypal.com"
	}

	// 1. Parse the incoming payload
	var payload Payload
	if Context.Req.BodyRaw() != "" {
		json.Unmarshal([]byte(Context.Req.BodyRaw()), &payload)
	}
	if payload.Currency == "" {
		payload.Currency = "USD"
	}

	// 2. Generate PayPal Access Token
	auth := base64.StdEncoding.EncodeToString([]byte(clientID + ":" + secret))
	tokenReq, _ := http.NewRequest("POST", baseURL+"/v1/oauth2/token", bytes.NewBuffer([]byte("grant_type=client_credentials")))
	tokenReq.Header.Add("Authorization", "Basic "+auth)
	tokenReq.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	tokenRes, err := client.Do(tokenReq)
	if err != nil || tokenRes.StatusCode != 200 {
		Context.Error("❌ Failed to authenticate with PayPal")
		return Context.Res.Json(map[string]interface{}{"error": "Authentication failed"})
	}
	defer tokenRes.Body.Close()

	var tokenData map[string]interface{}
	json.NewDecoder(tokenRes.Body).Decode(&tokenData)
	accessToken := tokenData["access_token"].(string)

	// 3. Create the PayPal Order
	orderPayload := map[string]interface{}{
		"intent": "CAPTURE",
		"purchase_units": []map[string]interface{}{
			{
				"amount": map[string]interface{}{
					"currency_code": payload.Currency,
					"value":         payload.Amount,
				},
			},
		},
		"application_context": map[string]interface{}{
			"return_url": "yourapp://paypal-success", // Intercepted by the client later
			"cancel_url": "yourapp://paypal-cancel",
		},
	}

	orderBody, _ := json.Marshal(orderPayload)
	orderReq, _ := http.NewRequest("POST", baseURL+"/v2/checkout/orders", bytes.NewBuffer(orderBody))
	orderReq.Header.Add("Authorization", "Bearer "+accessToken)
	orderReq.Header.Add("Content-Type", "application/json")

	orderRes, err := client.Do(orderReq)
	if err != nil || orderRes.StatusCode != 201 {
		Context.Error("❌ Failed to create PayPal order")
		return Context.Res.Json(map[string]interface{}{"error": "Failed to create order"})
	}
	defer orderRes.Body.Close()

	// 4. Extract the Approval URL
	var orderData map[string]interface{}
	json.NewDecoder(orderRes.Body).Decode(&orderData)

	var approveURL string
	links := orderData["links"].([]interface{})
	for _, link := range links {
		linkMap := link.(map[string]interface{})
		if linkMap["rel"] == "approve" {
			approveURL = linkMap["href"].(string)
			break
		}
	}

	Context.Log("✅ PayPal Order Created: " + orderData["id"].(string))

	// Return the Approval URL and the temporary Order ID
	return Context.Res.Json(map[string]interface{}{
		"approveUrl": approveURL,
		"orderId":    orderData["id"],
	})
}