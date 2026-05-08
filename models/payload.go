package models

// PaymentRequest is the unified request payload sent from the client (Flutter app).
// Not all fields are required for every gateway — each handler picks what it needs.
type PaymentRequest struct {
	Gateway       string `json:"gateway"`                 // Gateway identifier (for logging)
	Amount        int64  `json:"amount"`                  // Amount in smallest currency unit (cents, paise, poisha)
	Currency      string `json:"currency"`                // Currency code (e.g., "usd", "BDT", "INR")
	OrderId       string `json:"orderId"`                 // Your internal order/transaction ID
	CustomerName  string `json:"customerName,omitempty"`  // Required for SSLCommerz
	CustomerEmail string `json:"customerEmail,omitempty"` // Required for SSLCommerz
	CustomerPhone string `json:"customerPhone,omitempty"` // Required for SSLCommerz, bKash
	CallbackURL   string `json:"callbackURL,omitempty"`   // Callback URL for bKash execute
	PaymentID     string `json:"paymentID,omitempty"`     // Used for bKash execute step
}

// ErrorResponse is a standard error response returned by all gateway handlers.
type ErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// SuccessResponse wraps a successful gateway response with metadata.
type SuccessResponse struct {
	Success bool        `json:"success"`
	Gateway string      `json:"gateway"`
	Data    interface{} `json:"data"`
}
