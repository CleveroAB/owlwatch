# Troubleshooting

## The page says “unrecognized Host” or returns HTTP 421

owlwatch rejects unknown host names to prevent DNS-rebinding attacks. Add every domain used to reach it:

```sh
OWLWATCH_ALLOWED_HOSTS=monitor.example.com,owlwatch.lan
```

IP literals, `localhost`, and `*.localhost` work without configuration. Restart the container after changing the environment.

## The dashboard asks for a token repeatedly

- Confirm the browser token exactly matches `OWLWATCH_TOKEN` on the instance.
- In federation, confirm each `name=url|token` peer entry uses that peer's token.
- Confirm the reverse proxy forwards `Authorization` headers to REST and SSE endpoints.
- Check the browser console and `docker logs owlwatch` for 401 responses. Never paste the token into an issue or log excerpt.

## “Reconnecting…” never clears

1. Check `curl http://HOST:PORT/healthz` from the same network.
2. Confirm the reverse proxy supports long-lived Server-Sent Event responses and does not buffer them.
3. Increase proxy read timeouts beyond 60 seconds.
4. In hub mode, test the peer directly from the hub host.
5. Verify firewalls allow hub-to-peer TCP connections.

## Metrics describe the container instead of the host

Use the supplied Compose file or all bind mounts from the README. `HOST_PROC`, `HOST_SYS`, `HOST_ETC`, and `OWLWATCH_ROOTFS` must point to their matching mounted paths. Docker Desktop describes its Linux VM rather than the physical desktop and is not a supported host-monitoring target.

## Disk mounts are missing or stale

The host root mount must use `ro,rslave`. Without `rslave`, filesystems mounted after the container starts may not propagate into owlwatch. Some pseudo, temporary, and duplicate filesystems are intentionally filtered.

## NVIDIA GPU data is missing

- Run `nvidia-smi` successfully on the host first.
- Install NVIDIA Container Toolkit.
- Enable `gpus: all` in the Compose service or pass `--gpus all` to `docker run`.
- Restart owlwatch and inspect its startup log. AMD, Intel, and Apple GPUs are not supported in v1.

## History disappears after a restart

Persist `/data` with the named volume shown in the supplied Compose file. Verify `OWLWATCH_DB` points inside that volume (`/data/owlwatch.db` in the image). Deleting the volume intentionally resets history.

## Startup fails with “readonly database (1544)”

```
owlwatch: open store /data/owlwatch.db: store: /data is not writable
(uid 65532/gid 65532, directory owned by 0:0 mode 0755): permission denied
```

The container runs as the distroless `nonroot` user (uid 65532). The image
ships `/data` owned by that user, and Docker copies the ownership onto a
**fresh** named volume — but not onto one that already has content. A volume
created by a pre-1.0 release, which ran as root, therefore stays owned by
`0:0` and the nonroot process cannot create the database or its `-wal`/`-shm`
sidecars.

Fix the ownership of the existing volume on the host, keeping your history:

```sh
docker volume inspect <volume> --format '{{.Mountpoint}}'
sudo chown -R 65532:65532 "$(docker volume inspect <volume> --format '{{.Mountpoint}}')"
```

Find the volume name with `docker volume ls | grep owlwatch`; platforms that
namespace their resources (Coolify, for example) prefix it with a project id.
Restart the container afterwards. Discarding the history instead — deleting
the volume so a fresh, correctly-owned one is created — also works.

## The container is unhealthy immediately after startup

The health endpoint remains unavailable until the first sample arrives. Wait several sample intervals, then inspect:

```sh
docker inspect --format '{{json .State.Health}}' owlwatch
docker logs owlwatch
```

A persistent 503 means sampling has stalled or host mounts are inaccessible.

## Collecting safe diagnostics

Include the release version, deployment method, host OS/architecture, sanitized environment variable **names**, `/healthz` status, and relevant logs. Remove tokens, hostnames, IP addresses, peer URLs, and filesystem paths that reveal private information.
