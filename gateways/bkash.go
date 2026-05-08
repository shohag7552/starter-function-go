package gateways

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
)

const (
	bkashSandboxBase = "https://tokenized.sandbox.bka.sh/v1.2.0-beta"
	bkashLiveBase    = "https://tokenized.pay.bka.sh/v1.2.0-beta"
)

// HandleBkash handles bKash Tokenized Checkout.
// Routes:
//   POST /bkash/create  — Grant token + Create payment
//   POST /bkash/execute — Execute payment after user auth
// Env vars: BKASH_APP_KEY, BKASH_APP_SECRET, BKASH_USERNAME, BKASH_PASSWORD, BKASH_IS_SANDBOX
func HandleBkash(ctx openruntimes.Context) openruntimes.Response {
	path := ctx.Req.Path
	if strings.HasSuffix(path, "/execute") {
		return bkashExecute(ctx)
	}
	return bkashCreate(ctx)
}

func getBkashBaseURL() string {
	if s := os.Getenv("BKASH_IS_SANDBOX"); s == "" || s == "true" {
		return bkashSandboxBase
	}
	return bkashLiveBase
}

// bkashGrantToken gets an id_token from bKash token API.
func bkashGrantToken(ctx openruntimes.Context) (string, error) {
	base := getBkashBaseURL()
	appKey := os.Getenv("BKASH_APP_KEY")
	appSecret := os.Getenv("BKASH_APP_SECRET")
	username := os.Getenv("BKASH_USERNAME")
	password := os.Getenv("BKASH_PASSWORD")

	body := map[string]string{
		"app_key":    appKey,
		"app_secret": appSecret,
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", base+"/tokenized/checkout/token/grant", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("username", username)
	req.Header.Set("password", password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result map[string]interface{}
	if err = json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}

	token, _ := result["id_token"].(string)
	if token == "" {
		ctx.Error("❌ bKash token grant failed: " + string(respBody))
		return "", fmt.Errorf("failed to get bKash token")
	}
	return token, nil
}

// bkashCreate creates a payment and returns the bkashURL for user redirect.
func bkashCreate(ctx openruntimes.Context) openruntimes.Response {
	appKey := os.Getenv("BKASH_APP_KEY")
	if appKey == "" || os.Getenv("BKASH_APP_SECRET") == "" {
		ctx.Error("❌ bKash credentials missing")
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Server config error: missing bKash credentials",
		})
	}

	if ctx.Req.BodyRaw() == "" {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Request body is empty"})
	}

	type Payload struct {
		Amount      int64  `json:"amount"`
		OrderId     string `json:"orderId"`
		CallbackURL string `json:"callbackURL"`
	}
	var p Payload
	if err := json.Unmarshal([]byte(ctx.Req.BodyRaw()), &p); err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Invalid request format"})
	}
	if p.Amount <= 0 || p.OrderId == "" || p.CallbackURL == "" {
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "amount, orderId, and callbackURL are required",
		})
	}

	// Step 1: Grant token
	idToken, err := bkashGrantToken(ctx)
	if err != nil {
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Failed to get bKash token: " + err.Error(),
		})
	}

	// Step 2: Create payment
	base := getBkashBaseURL()
	// bKash expects amount as string (e.g., "15.00")
	amtStr := fmt.Sprintf("%.2f", float64(p.Amount)/100.0)

	createBody := map[string]string{
		"mode":                "0011",
		"payerReference":      " ",
		"callbackURL":         p.CallbackURL,
		"amount":              amtStr,
		"currency":            "BDT",
		"intent":              "sale",
		"merchantInvoiceNumber": p.OrderId,
	}
	jsonBody, _ := json.Marshal(createBody)

	req, err := http.NewRequest("POST", base+"/tokenized/checkout/create", bytes.NewBuffer(jsonBody))
	if err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Failed to create request"})
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", idToken)
	req.Header.Set("X-APP-Key", appKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ctx.Error("❌ bKash create payment failed: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Failed to connect to bKash"})
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Failed to read bKash response"})
	}

	var result map[string]interface{}
	if err = json.Unmarshal(respBody, &result); err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Invalid response from bKash"})
	}

	bkashURL, _ := result["bkashURL"].(string)
	paymentID, _ := result["paymentID"].(string)

	if bkashURL == "" || paymentID == "" {
		msg, _ := result["statusMessage"].(string)
		ctx.Error("❌ bKash create failed: " + msg)
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "bKash payment creation failed: " + msg})
	}

	ctx.Log("✅ bKash payment created. PaymentID: " + paymentID)

	return ctx.Res.Json(map[string]interface{}{
		"success": true, "gateway": "bkash",
		"data": map[string]interface{}{
			"paymentURL": bkashURL,
			"bkashURL":   bkashURL,
			"paymentID":  paymentID,
		},
	})
}

// bkashExecute finalizes the payment after user completes auth.
func bkashExecute(ctx openruntimes.Context) openruntimes.Response {
	appKey := os.Getenv("BKASH_APP_KEY")

	if ctx.Req.BodyRaw() == "" {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Request body is empty"})
	}

	type Payload struct {
		PaymentID string `json:"paymentID"`
	}
	var p Payload
	if err := json.Unmarshal([]byte(ctx.Req.BodyRaw()), &p); err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Invalid request format"})
	}
	if p.PaymentID == "" {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "paymentID is required"})
	}

	// Get fresh token
	idToken, err := bkashGrantToken(ctx)
	if err != nil {
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Failed to get bKash token: " + err.Error(),
		})
	}

	base := getBkashBaseURL()
	execBody := map[string]string{"paymentID": p.PaymentID}
	jsonBody, _ := json.Marshal(execBody)

	req, err := http.NewRequest("POST", base+"/tokenized/checkout/execute", bytes.NewBuffer(jsonBody))
	if err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Failed to create request"})
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", idToken)
	req.Header.Set("X-APP-Key", appKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Failed to connect to bKash"})
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Failed to read bKash response"})
	}

	var result map[string]interface{}
	if err = json.Unmarshal(respBody, &result); err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Invalid response from bKash"})
	}

	trxID, _ := result["trxID"].(string)
	statusMsg, _ := result["transactionStatus"].(string)

	ctx.Log("✅ bKash execute completed. TrxID: " + trxID + " Status: " + statusMsg)

	return ctx.Res.Json(map[string]interface{}{
		"success": true, "gateway": "bkash",
		"data": result,
	})
}
