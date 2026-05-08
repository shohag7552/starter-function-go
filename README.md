# ⚡ Multi-Gateway Payment Function (Go)

Appwrite Cloud Function that supports **5 payment gateways** in a single deployment:

| Gateway | Endpoint | What it returns |
|---------|----------|-----------------|
| **Stripe** | `POST /stripe/create` | `clientSecret` for Stripe SDK |
| **SSLCommerz** | `POST /sslcommerz/create` | `gatewayPageURL` for redirect |
| **bKash** | `POST /bkash/create` | `bkashURL` for redirect |
| **bKash** | `POST /bkash/execute` | Transaction result |
| **Razorpay** | `POST /razorpay/create` | `orderId` + `keyId` for Razorpay SDK |
| **PayPal** | `POST /paypal/create` | `approveURL` for redirect |

## ⚙️ Configuration

| Setting           | Value         |
| ----------------- | ------------- |
| Runtime           | Go (1.22+)    |
| Entrypoint        | `main.go`     |
| Permissions       | `any`         |
| Timeout (Seconds) | 15            |

## 🔒 Environment Variables

### Stripe
| Variable | Description |
|----------|-------------|
| `STRIPE_SECRET_KEY` | Stripe secret API key |

### SSLCommerz
| Variable | Description |
|----------|-------------|
| `SSLCOMMERZ_STORE_ID` | SSLCommerz store ID |
| `SSLCOMMERZ_STORE_PASSWORD` | SSLCommerz store password |
| `SSLCOMMERZ_IS_SANDBOX` | `true` for sandbox (default) |

### bKash
| Variable | Description |
|----------|-------------|
| `BKASH_APP_KEY` | bKash app key |
| `BKASH_APP_SECRET` | bKash app secret |
| `BKASH_USERNAME` | bKash merchant username |
| `BKASH_PASSWORD` | bKash merchant password |
| `BKASH_IS_SANDBOX` | `true` for sandbox (default) |

### Razorpay
| Variable | Description |
|----------|-------------|
| `RAZORPAY_KEY_ID` | Razorpay key ID |
| `RAZORPAY_KEY_SECRET` | Razorpay key secret |

### PayPal
| Variable | Description |
|----------|-------------|
| `PAYPAL_CLIENT_ID` | PayPal client ID |
| `PAYPAL_CLIENT_SECRET` | PayPal client secret |
| `PAYPAL_IS_SANDBOX` | `true` for sandbox (default) |

### Shared
| Variable | Description |
|----------|-------------|
| `PAYMENT_SUCCESS_URL` | Redirect URL on success (SSLCommerz/bKash) |
| `PAYMENT_FAIL_URL` | Redirect URL on failure (SSLCommerz/bKash) |
| `PAYMENT_CANCEL_URL` | Redirect URL on cancel (SSLCommerz/bKash) |

## 📂 Project Structure

```
├── main.go              # Router — dispatches to gateway handlers
├── gateways/
│   ├── stripe.go        # Stripe PaymentIntent
│   ├── sslcommerz.go    # SSLCommerz session
│   ├── bkash.go         # bKash tokenized checkout
│   ├── razorpay.go      # Razorpay order
│   └── paypal.go        # PayPal order
├── models/
│   └── payload.go       # Shared types
├── go.mod
└── go.sum
```

## 🧪 Example Requests

### Stripe
```bash
curl -X POST <FUNCTION_URL>/stripe/create \
  -H "Content-Type: application/json" \
  -d '{"amount": 1500, "currency": "usd", "orderId": "order_001"}'
```

### SSLCommerz
```bash
curl -X POST <FUNCTION_URL>/sslcommerz/create \
  -H "Content-Type: application/json" \
  -d '{"amount": 150000, "currency": "BDT", "orderId": "txn_001", "customerName": "John", "customerEmail": "john@mail.com", "customerPhone": "01700000000"}'
```

### bKash
```bash
# Step 1: Create payment
curl -X POST <FUNCTION_URL>/bkash/create \
  -H "Content-Type: application/json" \
  -d '{"amount": 50000, "orderId": "order_002", "callbackURL": "https://your-app.com/bkash/callback"}'

# Step 2: Execute payment (after user completes auth)
curl -X POST <FUNCTION_URL>/bkash/execute \
  -H "Content-Type: application/json" \
  -d '{"paymentID": "TR001234"}'
```

### Razorpay
```bash
curl -X POST <FUNCTION_URL>/razorpay/create \
  -H "Content-Type: application/json" \
  -d '{"amount": 50000, "currency": "INR", "orderId": "order_003"}'
```

### PayPal
```bash
curl -X POST <FUNCTION_URL>/paypal/create \
  -H "Content-Type: application/json" \
  -d '{"amount": 2500, "currency": "USD", "orderId": "order_004"}'
```
