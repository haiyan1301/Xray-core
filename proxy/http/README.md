# HTTP outbound headers

HTTP/1.1 CONNECT requests use the same header merge and serialization behavior
as [Mihomo's HTTP outbound](https://github.com/MetaCubeX/mihomo/blob/0455d0d28c4526937380505153e65a843d8612e8/adapter/outbound/http.go).

The client starts with these fields:

```http
Host: <target host:port>
User-Agent: Go-http-client/1.1
Proxy-Connection: Keep-Alive
```

It then merges `settings.headers` by exact, case-sensitive key. If both the
configured username and password are nonempty, the generated Basic
`Proxy-Authorization` field is applied last.

The HTTP/1.1 request is written directly. Header spelling, values, whitespace,
and empty values are preserved. Header order is unspecified. Use the exact
default spelling (`Host`, `User-Agent`, `Proxy-Connection`) to replace a default;
for example, `host` adds another field alongside `Host`, matching Mihomo.

## Changes from the previous Xray behavior

| Behavior | Previous Xray HTTP outbound | Current HTTP/1.1 behavior, matching Mihomo |
| --- | --- | --- |
| Header names | Canonicalized by `http.Header.Set` | Original spelling preserved; merge is case-sensitive |
| `Host` | Custom field ignored by `http.Request.Write`; target used | Exact `Host` key overrides the default without changing the CONNECT request target |
| `Proxy-Connection` | Always overwritten with `Keep-Alive` | Custom value can override the default |
| User-Agent and browser headers | Browser defaults and special values such as `chrome` or `firefox` expanded | Default `Go-http-client/1.1`; custom values sent literally; no browser headers added |
| Empty User-Agent | Suppresses the field | Sends an empty `User-Agent` field |
| Header values | Go templates expanded, then serialized by `net/http` | Literal strings, including `{{.Source}}` and `{{.Target}}` |
| Authentication | Custom headers override generated Basic authentication; an account can generate Basic authentication with an empty credential | Both credentials must be nonempty to generate Basic authentication; generated authentication overrides exact `Proxy-Authorization` |

For example, these outbound settings replace the HTTP/1.1 Host and User-Agent:

```json
{
  "protocol": "http",
  "settings": {
    "address": "proxy.example",
    "port": 8080,
    "headers": {
      "Host": "front.example",
      "User-Agent": "custom-agent/1.0",
      "Proxy-Connection": "close",
      "x-Custom-Header": "literal value"
    }
  }
}
```

The CONNECT request line still names the actual destination, for example
`CONNECT target.example:443 HTTP/1.1`. Changing the header does not change the
proxy server address or TLS server name.

## HTTP/2

Xray retains its HTTP/2 CONNECT support; the referenced Mihomo implementation
sends HTTP/1.1 CONNECT. HTTP/2 uses the same literal custom values, default
User-Agent, and authentication precedence, with protocol-specific encoding:

- Field names are encoded in lowercase by the HTTP/2 transport.
- CONNECT `:authority` remains the target address. Custom `Host` fields are
  omitted because HTTP/2 CONNECT has no independent HTTP/1.1 Host field.
- `Proxy-Connection` is omitted, including custom case variants, because HTTP/2
  forbids it.
- The HTTP/2 transport validates field names and values and omits an empty
  User-Agent instead of writing a raw HTTP/1.1 field.

The existing JSON and protobuf configuration formats are unchanged. Existing
configurations that depend on header templates, browser User-Agent presets, or
authentication with an empty credential must be updated for the new behavior.
