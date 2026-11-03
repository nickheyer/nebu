# Login

Signs the browser in, or creates the first account.

Route: `/login`, reached when `/auth/session` answers with no user and auth on. Returns to the page asked for.

## Default view

```
                     ◉ nebu

          ┌────────────────────────────┐
          │ Username                   │
          │ [                        ] │
          │ Password                   │
          │ [                        ] │
          │                            │
          │          [ Sign in ]       │
          │                            │
          │  ──────── or ────────      │
          │  [ Continue with Okta ]    │
          └────────────────────────────┘
```

One card, 360px wide, centred both ways. The brand over it. Nothing else.

## States

| Session answer | Card |
| --- | --- |
| pending | spinner, no card |
| unreachable | error line, Retry |
| `local.setup` | title Create the first account, username, password, password again |
| `local` | username, password |
| `sso` | Continue with `<name>` |
| `local` and `sso` | both, divided |
| refused | daemon error under the fields, password cleared |
| `enabled` false | never shown |

## Fields

| Field | Component | Bounds |
| --- | --- | --- |
| username | Text | 1 to 64 |
| password | Secret | 1 and up |

## Widths

Mobile: the card fills the width with 16px margins. Standard and ultrawide: 360px centred.

## Data

`GET /auth/session`, `POST /auth/local/login`, `POST /auth/local/setup`, `/auth/oidc/login`.
