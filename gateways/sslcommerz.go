package gateways

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/open-runtimes/types-for-go/v4/openruntimes"
)

const (
	sslSandboxURL    = "https://sandbox.sslcommerz.com/gwprocess/v3/api.php"
	sslProductionURL = "https://securepay.sslcommerz.com/gwprocess/v3/api.php"
)

// HandleSSLCommerz initiates a payment session with SSLCommerz.
// Endpoint: POST /sslcommerz/create
// Env vars: SSLCOMMERZ_STORE_ID, SSLCOMMERZ_STORE_PASSWORD, SSLCOMMERZ_IS_SANDBOX,
//           PAYMENT_SUCCESS_URL, PAYMENT_FAIL_URL, PAYMENT_CANCEL_URL
func HandleSSLCommerz(ctx openruntimes.Context) openruntimes.Response {
	storeID := os.Getenv("SSLCOMMERZ_STORE_ID")
	storePwd := os.Getenv("SSLCOMMERZ_STORE_PASSWORD")
	isSandbox := os.Getenv("SSLCOMMERZ_IS_SANDBOX")
	successURL := os.Getenv("PAYMENT_SUCCESS_URL")
	failURL := os.Getenv("PAYMENT_FAIL_URL")
	cancelURL := os.Getenv("PAYMENT_CANCEL_URL")

	if storeID == "" || storePwd == "" {
		ctx.Error("❌ SSLCommerz credentials missing")
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Server config error: missing SSLCommerz credentials",
		})
	}
	if successURL == "" || failURL == "" || cancelURL == "" {
		ctx.Error("❌ Payment callback URLs missing")
		return ctx.Res.Json(map[string]interface{}{
			"success": false, "error": "Server config error: missing callback URLs",
		})
	}

	if ctx.Req.BodyRaw() == "" {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Request body is empty"})
	}

	type Payload struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		OrderId  string `json:"orderId"`
		CusName  string `json:"customerName"`
		CusEmail string `json:"customerEmail"`
		CusPhone string `json:"customerPhone"`
	}
	var p Payload
	if err := json.Unmarshal([]byte(ctx.Req.BodyRaw()), &p); err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Invalid request format"})
	}
	if p.Amount <= 0 || p.OrderId == "" {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "amount and orderId are required"})
	}
	if p.Currency == "" {
		p.Currency = "BDT"
	}
	if p.CusName == "" {
		p.CusName = "Customer"
	}
	if p.CusEmail == "" {
		p.CusEmail = "customer@example.com"
	}
	if p.CusPhone == "" {
		p.CusPhone = "01700000000"
	}

	apiURL := sslProductionURL
	if isSandbox == "" || isSandbox == "true" {
		apiURL = sslSandboxURL
	}

	amtStr := fmt.Sprintf("%.2f", float64(p.Amount)/100.0)
	form := url.Values{
		"store_id": {storeID}, "store_passwd": {storePwd},
		"total_amount": {amtStr}, "currency": {p.Currency}, "tran_id": {p.OrderId},
		"success_url": {successURL}, "fail_url": {failURL}, "cancel_url": {cancelURL},
		"cus_name": {p.CusName}, "cus_email": {p.CusEmail}, "cus_phone": {p.CusPhone},
		"cus_add1": {"N/A"}, "cus_city": {"N/A"}, "cus_country": {"Bangladesh"},
		"shipping_method": {"NO"}, "product_name": {"Order " + p.OrderId},
		"product_category": {"general"}, "product_profile": {"general"},
	}

	resp, err := http.PostForm(apiURL, form)
	if err != nil {
		ctx.Error("❌ SSLCommerz request failed: " + err.Error())
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Failed to connect to SSLCommerz"})
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Failed to read gateway response"})
	}

	var sslResp map[string]interface{}
	if err = json.Unmarshal(body, &sslResp); err != nil {
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "Invalid response from SSLCommerz"})
	}

	status, _ := sslResp["status"].(string)
	if status != "SUCCESS" {
		reason, _ := sslResp["failedreason"].(string)
		ctx.Error("❌ SSLCommerz failed: " + reason)
		return ctx.Res.Json(map[string]interface{}{"success": false, "error": "SSLCommerz failed: " + reason})
	}

	gwURL, _ := sslResp["GatewayPageURL"].(string)
	sessKey, _ := sslResp["sessionkey"].(string)
	ctx.Log("✅ SSLCommerz session created for Order: " + p.OrderId)

	return ctx.Res.Json(map[string]interface{}{
		"success": true, "gateway": "sslcommerz",
		"data": map[string]interface{}{
			"gatewayPageURL": gwURL, "sessionKey": sessKey, "status": status,
		},
	})
}
