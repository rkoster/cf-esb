# CF-ESB

CF-ESB is a lightweight Open Service Broker for ephemeral services running as Cloud Foundry apps. The initial catalog contains PostgreSQL and Garage. Service definitions and fixed credentials are packaged with the broker in `config/services.yml`; provisioning and binding state are derived from Cloud Foundry apps, credential bindings, routes, and network policies. The broker itself has no persistent store.

## PostgreSQL MVP behavior

- Provisioning creates `cfe-<service-id>-<instance-id>` as a Docker-lifecycle app in the configured managed space, creates a Docker package/build, and injects the configured environment variables. OSB consumer-space metadata does not determine the service app's space.
- Binding installs a TCP network policy from the client app to PostgreSQL on the configured port and returns the static credentials configured in YAML.
- Unbinding removes the matching policy using binding-ID metadata stored on the CF service app. This lookup is derived from Cloud Foundry app metadata, so the broker keeps no local binding table. The broker assumes one binding per app/service-instance pair.
- Deprovisioning removes policies targeting the instance and deletes its app.
- PostgreSQL is exposed through a Cloud Foundry internal route only; it is not exposed on a public route.

The static demo password is intentionally shared across PostgreSQL instances. This is suitable only for labs and development environments. Change it in `config/services.yml` before deploying in any environment where that default is not acceptable.

## Garage MVP behavior

Garage is configured as an ephemeral, single-node S3-compatible service using the `dxflrs/garage:v2.3.0` Docker image. It uses direct `apps.internal` app DNS with a TCP/3900 network policy (not an HTTP route). The service YAML provides a runtime command that writes the Garage TOML config, uses Garage's `--single-node --default-bucket` setup, and returns the configured static S3 endpoint, bucket, access key and secret key in each binding.

The Garage metadata and data directories currently live in `/tmp` in the app container; CF restarts, restaging, or replacement erase the object data. All instances use the same configured demonstration credentials and bucket name. This is for labs and demos only, not production. The app is reachable on TCP 3900 using the configured internal route domain.

## Configuration

### Broker environment variables

| Variable | Required | Description |
| --- | --- | --- |
| `CF_API_URL` | yes | Cloud Controller API URL, for example `https://api.example.org` |
| `CF_LOGIN_URL` | yes | Login server URL, for example `https://login.example.org` |
| `CF_UAA_URL` | yes | UAA URL, for example `https://uaa.example.org` |
| `CF_CLIENT_ID` | yes | UAA client with Cloud Controller and network policy access |
| `CF_CLIENT_SECRET` | yes | UAA client secret |
| `CF_ORG` | recommended | Organization containing the managed space; disambiguates same-named spaces |
| `CF_SPACE` | yes | Space where the broker app is deployed |
| `CF_INSTANCE_SPACE` | yes | Space where PostgreSQL service apps and internal routes are created |
| `CF_SKIP_TLS_VALIDATION` | no | Set to `true` only for lab foundations with untrusted certificates. Applies to both CAPI and CF Networking calls. |
| `BROKER_USERNAME` | yes for production | HTTP Basic username for OSB requests |
| `BROKER_PASSWORD` | yes for production | HTTP Basic password for OSB requests |
| `SERVICES_CONFIG` | no | Service config path; defaults to `config/services.yml` |
| `PORT` | no | HTTP listen port; defaults to `8080` |

The CF client requires an OAuth client with permissions to create/start/delete apps, manage routes in the configured space, read credential bindings, and create/delete CF Networking policies (`network.write` or `network.admin`, depending on the foundation). Both `CF_API_URL` and `CF_UAA_URL` are supplied explicitly.

### Packaged service YAML

`config/services.yml` is read at broker startup and packaged with the app. It defines catalog metadata, image, port, container environment, plans, fixed binding credentials, and the URI/host template. Adding a service is intended to be a YAML-only operation as long as it follows the shared service schema.

The `environment` values are injected literally into the service app. Keep the corresponding binding credentials in sync with the image’s initialization variables. For PostgreSQL this means `POSTGRES_DB`, `POSTGRES_USER`, and `POSTGRES_PASSWORD` match the credentials returned on bind.

## Local development

With Devbox installed:

```sh
devbox shell
go test ./...
go run ./cmd/cf-esb
```

To push the broker and create or update its global Cloud Foundry service-broker registration, run `devbox run deploy`. It also enables PostgreSQL marketplace access for the configured org when access is not already enabled. `CF_SPACE` is the broker app space; `CF_INSTANCE_SPACE` is the separate space used for PostgreSQL apps. The task requires the Cloud Foundry CLI to be logged in and targeted access to the configured spaces, plus the ignored local `.secrets` vars file. Re-running it updates the app and registration and leaves already-enabled access unchanged.

The broker fails fast when required CF settings or the managed space are unavailable. `GET /health` is available for liveness checks. All OSB endpoints use the `/v2` prefix and Basic authentication when broker credentials are configured.

## Deploying to Cloud Foundry

Build with the Go buildpack, then push the repository using `manifest.yml`. Set the CF and broker credentials with `cf set-env` (or the platform’s secret-management mechanism), and ensure `config/services.yml` is included in the pushed app. The CF account used by the broker must target the foundation and have rights to the configured broker space.

Example catalog request after deployment:

```sh
curl -u "$BROKER_USERNAME:$BROKER_PASSWORD" "https://<broker-route>/v2/catalog"
```

## Current MVP boundaries

- PostgreSQL instances and credentials are ephemeral and have no backup/restore guarantee.
- Static credentials are shared by all instances of the configured service.
- One app may have only one binding to a given service instance.
- New service app Docker package/build staging uses OSB `202 Accepted`; callers must send `accepts_incomplete=true` and poll `last_operation` until the app is started.
- The foundation must provide CF internal app service discovery (for `apps.internal`) to resolve the PostgreSQL endpoint.
- CAPI network policy endpoints require a foundation with CF Networking enabled and appropriate policy-server authorization.
