# Secure deployment recipes

owlwatch serves HTTP. Use these examples when access crosses an untrusted network. In every case, generate a strong token first:

```sh
openssl rand -base64 32
```

Store it as `OWLWATCH_TOKEN`; never commit it. Add the public host name to `OWLWATCH_ALLOWED_HOSTS`.

## Caddy

```caddyfile
monitor.example.com {
    reverse_proxy 127.0.0.1:8080

    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
    }
}
```

```sh
export OWLWATCH_TOKEN="$(openssl rand -base64 32)"
export OWLWATCH_ALLOWED_HOSTS=monitor.example.com
docker compose up -d
```

Caddy provisions and renews HTTPS automatically when DNS points at the server and ports 80/443 are reachable.

## nginx

```nginx
server {
    listen 443 ssl http2;
    server_name monitor.example.com;

    # Configure ssl_certificate and ssl_certificate_key for your environment.
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE must not be buffered or cut off by a short timeout.
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 1h;
    }
}
```

## Tailscale-only access

Bind the published Docker port to the host's Tailscale address or firewall it so only the Tailscale interface can reach it. Keep `OWLWATCH_TOKEN` enabled for defense in depth. You may also place `tailscale serve` in front to terminate HTTPS.

## Reverse-proxy logging

The web client authenticates REST and streaming requests with an
`Authorization: Bearer` header; credentials are never placed in URLs. Do not
configure a reverse proxy to record authorization headers.

## Verification

After deployment, with `OWLWATCH_TOKEN` still set in the shell:

```sh
curl -fsS https://monitor.example.com/healthz
curl -i https://monitor.example.com/api/servers
curl -fsS -H "Authorization: Bearer ${OWLWATCH_TOKEN}" \
  https://monitor.example.com/api/servers
```

The second request must return `401`; the authenticated request must return JSON. Check the browser network panel for a long-lived `text/event-stream` response and confirm the dashboard remains **Live** for more than a minute.
