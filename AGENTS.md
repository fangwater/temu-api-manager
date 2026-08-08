# Temu API Manager

## API Routing

- Send application requests to `http://13.115.227.29:6355/openapi/router`.
- The reverse proxy forwards requests to `https://openapi-b-us.temu.com/openapi/router`.
- Do not call the upstream URL directly from application code unless explicitly requested.
- Keep the runtime endpoint configurable through `TEMU_API_BASE_URL`.

## Credentials

- Real credentials are stored only in the local `.env` file.
- Use `TEMU_ACCESS_TOKEN`, `TEMU_APP_KEY`, and `TEMU_APP_SECRET` to load credentials.
- Never copy credential values into source code, tests, documentation, logs, or command output.
- Keep `.env` ignored by Git and restricted to the current user.

## Connectivity Check

- A credential-free POST to the proxy returned HTTP 200 from the Temu gateway on 2026-07-13.
- Temu returned error `3000002` (`there is no type in body.`), confirming that the proxy reached the upstream API.

## API Implementation

- Temu request signatures use uppercase MD5.
- Sort all request keys except `sign` in ascending ASCII order, concatenate each key and serialized value, then prepend and append `TEMU_APP_SECRET` before hashing.
- Use `temu-manager order-list` for `bg.order.list.v2.get`.
- Use `temu-manager order-detail` for `bg.order.detail.v2.get`; it requires a parent order number.
- Use `temu-manager order-amount` for the sensitive `bg.order.amount.query` API; it requires separate Temu approval and a parent order number.
- Use `temu-manager baseprice-recommend` for `temu.local.goods.baseprice.recommend`; pass the nested product request with `--params-file`.
- Raw order exports can contain sensitive business data and must be written with file mode `600`.
