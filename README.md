# Stripe Payment Function (Go)

An Appwrite Cloud Function that handles Stripe payments for a client app.

It uses **Stripe Checkout (hosted)**: the customer enters their card on
`checkout.stripe.com`, so no card data ever touches this function or your app.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/stripe/create` | `x-api-secret` | Open a Checkout Session, returns the hosted payment URL |
| POST | `/stripe/verify` | `x-api-secret` | Ask Stripe whether a session was actually paid |
| POST | `/stripe/webhook` | Stripe signature | Receive Stripe's event notifications |
| POST | `/debug` | `x-api-secret` | Configuration report (off unless enabled) |

> **The one rule:** creating a session is not a payment, and landing on your
> success URL is not a payment. Only mark an order paid after `/stripe/verify`
> or the webhook confirms Stripe actually charged the card.

Full reference — every environment variable, the webhook setup, the Flutter
flow, error codes and the go-live checklist — is in **[docs/STRIPE.md](docs/STRIPE.md)**.

## Appwrite settings

| Setting | Value |
|---|---|
| Runtime | Go 1.23+ |
| Entrypoint | `main.go` |
| Permissions | `any` — Stripe's servers must reach the webhook route |
| Timeout (seconds) | 15 |

## Minimum environment variables

Enough to take a test payment:

| Variable | Value |
|---|---|
| `API_SECRET` | A long random string. **Required** — the function refuses every request without it. |
| `STRIPE_SECRET_KEY` | Your `sk_test_…` key |
| `STRIPE_WEBHOOK_SECRET` | The `whsec_…` signing secret from the Stripe dashboard |
| `PAYMENT_SUCCESS_URL` | Where the customer lands after paying |
| `PAYMENT_CANCEL_URL` | Where the customer lands if they back out |

`PAYMENT_MODE` defaults to `test`, and a `sk_live_` key in test mode is refused —
a missing or misspelled variable can never silently start charging real cards.
Set `PAYMENT_MODE=live` and `STRIPE_LIVE_SECRET_KEY` when you go live.

## Example

```bash
# 1. Create the session
curl -X POST <FUNCTION_URL>/stripe/create \
  -H "Content-Type: application/json" \
  -H "x-api-secret: $API_SECRET" \
  -d '{"amount": 1500, "currency": "USD", "orderId": "order_001", "productName": "Chicken Biryani x2"}'
# → { "data": { "paymentURL": "https://checkout.stripe.com/…", "sessionId": "cs_test_…" } }

# 2. Open paymentURL, let the customer pay, then verify before releasing the order
curl -X POST <FUNCTION_URL>/stripe/verify \
  -H "Content-Type: application/json" \
  -H "x-api-secret: $API_SECRET" \
  -d '{"sessionId": "cs_test_…", "orderId": "order_001"}'
# → { "data": { "paid": true, "status": "paid", … } }
```

`amount` is in **minor units** (1500 = $15.00) and `currency` is required — it
is never defaulted, so a client that omits it gets a `400` rather than a charge
in the wrong currency.

## Project structure

```
├── main.go              # Router — auth, method and route matching
├── main_test.go
├── gateways/
│   ├── stripe.go        # create / verify / webhook
│   └── stripe_test.go
├── docs/STRIPE.md       # Full integration guide
├── go.mod
└── go.sum
```

## Tests

```bash
go test ./...
```
