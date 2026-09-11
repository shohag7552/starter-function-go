# Stripe Integration Guide

Production setup for the Stripe gateway in this Appwrite function.

**Integration type:** Stripe Checkout Session (hosted). The customer enters their
card on `checkout.stripe.com`, never in your app or on your server, which keeps
this integration in Stripe's simplest PCI tier (SAQ A).

---

## 1. The one rule

**Creating a session is not a payment. Landing on your success URL is not a payment.**

`/stripe/create` only reserves an intent to collect. The success URL is just a
URL — anyone can open it directly, and a card can still be declined at the final
moment. An order may only be marked paid after **`/stripe/verify`** or the
**webhook** confirms Stripe actually charged the card.

For a food delivery system that is the difference between "customer paid" and
"customer saw a thank-you screen".

---

## 2. Endpoints

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/stripe/create` | `x-api-secret` | Create a Checkout Session, returns hosted payment URL |
| POST | `/stripe/verify` | `x-api-secret` | Ask Stripe whether a session was actually paid |
| POST | `/stripe/webhook` | Stripe signature | Receive Stripe's event notifications |

The webhook route is exempt from `x-api-secret` because Stripe cannot send a
custom header. It authenticates with Stripe's HMAC signature instead, verified
against `STRIPE_WEBHOOK_SECRET`.

---

## 3. Environment variables

### Mode

| Variable | Values | Default | Purpose |
|---|---|---|---|
| `PAYMENT_MODE` | `test` / `live` | `test` | System-wide mode |
| `STRIPE_MODE` | `test` / `live` | inherits `PAYMENT_MODE` | Stripe-only override |

Mode defaults to `test`. A missing or misspelled variable can never silently
start charging real cards.

### Credentials

Test and live credentials can sit side by side. The mode-specific name is
preferred; the unprefixed name is the fallback.

| Variable | Required | Notes |
|---|---|---|
| `STRIPE_TEST_SECRET_KEY` | test mode | `sk_test_…` |
| `STRIPE_LIVE_SECRET_KEY` | live mode | `sk_live_…` |
| `STRIPE_SECRET_KEY` | fallback | Used if the mode-specific one is unset |
| `STRIPE_TEST_WEBHOOK_SECRET` | test webhooks | `whsec_…` from the Stripe dashboard |
| `STRIPE_LIVE_WEBHOOK_SECRET` | live webhooks | `whsec_…` — **different from the test one** |
| `STRIPE_WEBHOOK_SECRET` | fallback | Used if the mode-specific one is unset |

**Key guard:** an `sk_live_` key in test mode (or `sk_test_` in live mode) is
refused with a config error. This is what stops real cards being charged during
QA. Override with `PAYMENT_ALLOW_KEY_MISMATCH=true` only if you have a genuine
reason.

### Redirect URLs

| Variable | Required | Notes |
|---|---|---|
| `PAYMENT_SUCCESS_URL` | yes | Stripe appends `?session_id={CHECKOUT_SESSION_ID}` |
| `PAYMENT_CANCEL_URL` | yes | Where the customer lands if they back out |
| `PAYMENT_TEST_SUCCESS_URL` | optional | Overrides the above in test mode |
| `PAYMENT_TEST_CANCEL_URL` | optional | Overrides the above in test mode |

A URL that already has a query string is handled correctly — `?ref=abc` becomes
`?ref=abc&session_id=…`, and fragments stay last.

Custom schemes work: `myapp://payment/success` is valid.

### Limits and general

| Variable | Default | Purpose |
|---|---|---|
| `API_SECRET` | **required** | Shared secret for all non-webhook routes |
| `STRIPE_MIN_AMOUNT` | `50` | Minor units. Stripe rejects charges under ~$0.50 |
| `PAYMENT_MAX_AMOUNT` | `100000000` | Sanity ceiling; catches a client sending cents twice |
| `ENABLE_DEBUG_ENDPOINT` | unset | Set to `true` to expose `/debug` |
| `PAYMENT_ALLOW_KEY_MISMATCH` | unset | Disables the live/test key guard |

---

## 4. API reference

### POST /stripe/create

```jsonc
{
  "amount": 1500,                        // required, minor units: 1500 = $15.00
  "currency": "USD",                     // required, ISO 4217 — NOT defaulted
  "orderId": "order_123",                // required, your order ID
  "productName": "Chicken Biryani x2",   // optional, shown on the Stripe page
  "description": "Order from Spice Hub", // optional
  "customerEmail": "a@b.com",            // optional, prefills the Stripe form
  "successURL": "myapp://pay/ok",        // optional, overrides the env var
  "cancelURL": "myapp://pay/cancel",     // optional
  "metadata": {                          // optional, attached to the payment
    "restaurantId": "rest_9",
    "riderZone": "dhanmondi"
  }
}
```

**`currency` is required.** It used to default to `"usd"`, which meant a client
that forgot the field turned a ৳1,500 order into a **$15.00** charge, silently.

Response `200`:

```json
{
  "success": true,
  "gateway": "stripe",
  "mode": "test",
  "data": {
    "paymentURL": "https://checkout.stripe.com/c/pay/cs_test_…",
    "sessionId": "cs_test_…",
    "orderId": "order_123",
    "amount": 1500,
    "amountDisplay": "15.00",
    "currency": "USD",
    "expiresAt": 1767225600,
    "requiresVerify": true
  }
}
```

The session stays open for **24 hours**, then expires.

**Idempotency:** a retried create for the same order, amount and currency
returns the original session rather than opening a second one — safe for network
retries. A genuinely changed cart amount produces a new session.

### POST /stripe/verify

```json
{ "sessionId": "cs_test_…", "orderId": "order_123" }
```

`orderId` is optional. Supply it and the function checks Stripe agrees — a
mismatch returns `409 order_mismatch`, so a client cannot confirm someone else's
session against your order.

Response `200`:

```json
{
  "success": true,
  "gateway": "stripe",
  "mode": "test",
  "data": {
    "sessionId": "cs_test_…",
    "orderId": "order_123",
    "paid": true,
    "status": "paid",
    "paymentStatus": "paid",
    "sessionStatus": "complete",
    "amount": 1500,
    "amountDisplay": "15.00",
    "currency": "USD",
    "paymentIntentId": "pi_…",
    "customerEmail": "a@b.com"
  }
}
```

**Gate your order on `paid`, not on `status`.**

| `status` | `paid` | Meaning | Action |
|---|---|---|---|
| `paid` | `true` | Card charged | Release the order |
| `no_payment_required` | `true` | Zero-value order, fully discounted | Release the order |
| `pending` | `false` | Session still open, customer hasn't finished | Wait |
| `processing` | `false` | Checkout done, bank debit still clearing | **Wait** — do not release |
| `expired` | `false` | 24h passed unpaid | Cancel the order |

`processing` is the subtle one. The customer finished checkout, but a delayed
payment method has not settled and can still fail. Releasing food here means
cooking against a payment that may never arrive — wait for the webhook.

The endpoint returns `200` even for unpaid sessions: the verification itself
succeeded, and `paid` carries the outcome.

### POST /stripe/webhook

Called by Stripe, not by your app. Handles:

- `checkout.session.completed`
- `checkout.session.async_payment_succeeded`
- `checkout.session.async_payment_failed`
- `checkout.session.expired`

Rejects with `400` on a bad or missing signature, a replayed event (Stripe's
timestamp tolerance), or an event whose livemode does not match the deployment's
mode. Unknown event types are acknowledged with `200` so Stripe does not retry
them for three days and eventually disable your endpoint.

---

## 5. Setup

### Appwrite

| Setting | Value |
|---|---|
| Runtime | Go 1.23+ |
| Entrypoint | `main.go` |
| Permissions | `any` — Stripe's servers must reach the webhook route |
| Timeout | 15 seconds |

The outbound Stripe timeout is 10s, deliberately below the function's 15s. If
the HTTP call could outlive the function, the container would be killed
mid-request and your client would get nothing back while Stripe may already have
created the session.

### Stripe dashboard — webhook

1. **Developers → Webhooks → Add endpoint**
2. URL: `https://<your-function>.appwrite.global/stripe/webhook`
3. Events: the four `checkout.session.*` events listed above
4. Copy the **signing secret** (`whsec_…`) into `STRIPE_TEST_WEBHOOK_SECRET`

Test mode and live mode have **separate endpoints with separate signing
secrets**. Register both, and put each secret in its own variable.

Local testing without a public URL:

```bash
stripe listen --forward-to https://<your-function>.appwrite.global/stripe/webhook
stripe trigger checkout.session.completed
```

---

## 6. Flutter flow

```dart
// 1. Create the session
final res = await http.post(
  Uri.parse('$functionUrl/stripe/create'),
  headers: {
    'Content-Type': 'application/json',
    'x-api-secret': apiSecret,
  },
  body: jsonEncode({
    'amount': 1500,          // minor units
    'currency': 'USD',       // required
    'orderId': order.id,
    'productName': order.summary,
  }),
);

// Errors now carry real HTTP status codes.
if (res.statusCode != 200) {
  final err = jsonDecode(res.body)['error'];
  throw PaymentException(err['code'], err['message']);
}

final data = jsonDecode(res.body)['data'];
final paymentUrl = data['paymentURL'];
final sessionId  = data['sessionId'];

// 2. Open the hosted page and wait for the redirect
await launchInWebView(paymentUrl);   // watch for PAYMENT_SUCCESS_URL / CANCEL_URL

// 3. Verify before doing anything with the order
final check = await http.post(
  Uri.parse('$functionUrl/stripe/verify'),
  headers: {
    'Content-Type': 'application/json',
    'x-api-secret': apiSecret,
  },
  body: jsonEncode({'sessionId': sessionId, 'orderId': order.id}),
);

final result = jsonDecode(check.body)['data'];

if (result['paid'] == true) {
  // Safe to confirm the order
} else if (result['status'] == 'processing') {
  // Show "payment confirming" — the webhook will settle it
} else {
  // Not paid: expired, cancelled, or failed
}
```

Keep `sessionId` in your order record. If the app is killed between steps 2 and
3, you can verify later instead of losing the payment.

---

## 7. Testing

Flip the Stripe Dashboard's **Test mode** toggle — test payments only appear there.

| Card | Result |
|---|---|
| `4242 4242 4242 4242` | Succeeds |
| `4000 0000 0000 0002` | Declined (generic) |
| `4000 0000 0000 9995` | Declined (insufficient funds) |
| `4000 0025 0000 3155` | Requires 3D Secure authentication |
| `4000 0000 0000 0341` | Attaches, then fails when charged |

Any future expiry, any 3-digit CVC, any postal code.

Run the test suite with:

```bash
go test ./...
```

---

## 8. Going live

- [ ] Set `STRIPE_LIVE_SECRET_KEY` to your `sk_live_…` key
- [ ] Register a **live** webhook endpoint in the Stripe dashboard
- [ ] Set `STRIPE_LIVE_WEBHOOK_SECRET` to the live endpoint's `whsec_…`
- [ ] Set `PAYMENT_SUCCESS_URL` / `PAYMENT_CANCEL_URL` to production deep links
- [ ] Set `PAYMENT_MODE=live`
- [ ] Confirm `API_SECRET` is set and is a long random value
- [ ] Confirm `ENABLE_DEBUG_ENDPOINT` is **not** `true`
- [ ] Confirm `PAYMENT_ALLOW_KEY_MISMATCH` is **not** set
- [ ] Verify your app treats any non-200 as a failure
- [ ] Verify your app gates order release on `paid == true`, never on the redirect
- [ ] Run one real low-value transaction end to end, then refund it

---

## 9. Error codes

| HTTP | `error.code` | Meaning |
|---|---|---|
| 400 | `invalid_request` | Missing or malformed field |
| 400 | `amount_too_small` | Below `STRIPE_MIN_AMOUNT` |
| 400 | `amount_too_large` | Above `PAYMENT_MAX_AMOUNT` |
| 400 | `invalid_signature` | Webhook signature failed or missing |
| 400 | `mode_mismatch` | Live event hit a test deployment, or vice versa |
| 401 | `unauthorized` | Bad or missing `x-api-secret` |
| 402 | *(Stripe code)* | Card declined |
| 404 | `unknown_route` | No such route, or `/debug` while disabled |
| 405 | `method_not_allowed` | Anything other than POST |
| 409 | `order_mismatch` | Session belongs to a different order |
| 500 | `config_error` | Missing key, missing URLs, key/mode mismatch |
| 502 | `gateway_unreachable` | Stripe did not respond in time |

---

## 10. Migration notes — breaking changes

If your Flutter app already talks to the old version, these will break it.

**1. Error responses changed shape and status code.**

```jsonc
// before — always HTTP 200
{ "success": false, "error": "Amount must be greater than 0" }

// now — real status codes
{ "success": false, "error": { "code": "invalid_request", "message": "…" } }
```

**2. `currency` is now required** on `/stripe/create`. It is no longer defaulted
to `"usd"`.

**3. `API_SECRET` is now mandatory.** Authentication used to skip itself when the
variable was empty; it now refuses every request with a config error. Set it
before deploying.

**4. `/debug` is off** unless `ENABLE_DEBUG_ENDPOINT=true`, and it no longer
returns the first four characters of each secret — only whether it is set.

**5. Route matching is exact.** `/stripe-anything` used to reach the Stripe
handler and now returns 404.

---

## 11. Wiring the webhook to your orders

This function is stateless by design, so the webhook verifies the event, logs
it, and returns the result — it does not write to a database. The extension
point is marked in [`gateways/stripe.go`](../gateways/stripe.go) inside
`stripeWebhook`.

Whatever you connect it to, two rules:

1. **Be idempotent.** Stripe redelivers events. Setting an already-paid order to
   paid must be a no-op.
2. **Stay fast and always answer 2xx.** A slow or failing handler makes Stripe
   retry, and repeated failures disable the endpoint.
