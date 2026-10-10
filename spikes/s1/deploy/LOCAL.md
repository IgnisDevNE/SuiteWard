# Co-located run (this PC, Podman)

The co-located target runs the same image next to the developer's tools. The App key stays under `C:\Users\magal\.suiteward-s1\` and is mounted read-only; it is never passed on a command line or in an environment variable, and it is not given to the agent. The folder is readable by other tools running as you on this PC; that is the accepted co-located tier (D-TRUST).

## One time

Create `C:\Users\magal\.suiteward-s1\` and put two files in it yourself:

- `app-key.pem`: the test App private key.
- `env`: one `NAME=value` per line, no quotes, no `export`:

```
S1_APP_ID=5257122
S1_INSTALLATION_ID=169774099
S1_REPO=IgnisDevNE/SuiteWardQ
S1_OWNER_LOGIN=magalz
S1_TOKEN_PERMISSIONS=checks:write,pull_requests:read,contents:read
```

## Run

Pull the published image (after the first publish, and after the GHCR package is made public, see `HOST.md`) or build it locally from `spikes/s1/`:

```powershell
podman build -t localhost/suiteward-s1:local -f spikes/s1/Containerfile spikes/s1
```

```powershell
podman volume create suiteward-s1-data
podman run -d --name suiteward-s1 --restart=always `
  --read-only --cap-drop=all --security-opt no-new-privileges `
  --env-file C:\Users\magal\.suiteward-s1\env `
  -e S1_STATUS_ADDR=0.0.0.0:8080 `
  -v C:\Users\magal\.suiteward-s1\app-key.pem:/run/secrets/app-key:ro `
  -v suiteward-s1-data:/data `
  -p 127.0.0.1:8080:8080 `
  ghcr.io/ignisdevne/suiteward-s1:deploy
```

Use `localhost/suiteward-s1:local` instead of the GHCR name for a local build. The root filesystem is read-only; the only writable path is the `/data` volume (the program does not need `/tmp`).

## Observe

```powershell
podman logs -f suiteward-s1       # JSON lines
curl http://127.0.0.1:8080/status
podman restart suiteward-s1       # restart measurement; state survives in the volume
```

On a Podman machine backed by WSL the host loopback forward may not carry a `127.0.0.1` publish. If `curl` cannot connect, test from inside the machine instead: `podman machine ssh "curl -s http://127.0.0.1:8080/status"`.

Stop with `podman rm -f suiteward-s1`; `podman volume rm suiteward-s1-data` drops the state.
