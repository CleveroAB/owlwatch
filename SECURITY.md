# Security policy

## Supported versions

Security fixes are provided for the latest stable release of owlwatch. Upgrade to the newest release before reporting a problem that may already be fixed.

| Version | Supported |
|---|---|
| Latest stable release | ✅ |
| Older releases and development snapshots | ❌ |

## Reporting a vulnerability

Please **do not open a public issue** for a suspected vulnerability.

Use GitHub's private vulnerability reporting form:

https://github.com/CleveroAB/owlwatch/security/advisories/new

Include the affected version, deployment setup, reproduction steps, impact, and any suggested mitigation. We aim to acknowledge a complete report within 3 business days and will coordinate disclosure and credit with the reporter.

## Deployment security boundary

owlwatch exposes host telemetry and does not terminate TLS. Treat metrics as sensitive:

- set `OWLWATCH_TOKEN` to a long, random value;
- run owlwatch on a trusted LAN or VPN, or behind an HTTPS reverse proxy;
- never expose its HTTP port directly to the public internet;
- keep `/proc`, `/sys`, `/etc`, and `/` mounts read-only as shown in the supplied Compose file;
- restrict access to the Docker socket—owlwatch neither needs nor mounts it.

The project warning and supported controls are documented in [README.md](README.md#security-and-exposure).
