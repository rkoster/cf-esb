# CF-ESB

CF-ESB is a lightweight Open Service Broker for ephemeral services running as Cloud Foundry apps. The catalog contains PostgreSQL, Redis, and Garage. Service definitions and fixed credentials are packaged with the broker in `config/services.yml`; provisioning and binding state are derived from Cloud Foundry apps, credential bindings, routes, and network policies. The broker itself has no persistent store.

## PostgreSQL MVP behavior

- Provisioning creates `cfe-<service-id>-<instance-id>` as a Docker-lifecycle app in the configured managed space, creates a Docker package/build, and injects the configured environment variables. OSB consumer-space metadata does not determine the service app's space.
- Binding installs a TCP network policy from the client app to PostgreSQL on the configured port and returns the static credentials configured in YAML.
- Unbinding removes the matching policy using binding-ID metadata stored on the CF service app. This lookup is derived from Cloud Foundry app metadata, so the broker keeps no local binding table. The broker assumes one binding per app/service-instance pair.
- Deprovisioning removes policies targeting the instance and deletes its app.
- Deprovisioning requires OSB `accepts_incomplete=true`; it removes policies, starts asynchronous CAPI app deletion, returns the CAPI job GUID as the OSB operation, and reports job progress/failure through `last_operation`.
- PostgreSQL uses a portless Cloud Foundry internal route for service-discovery DNS only; it is not exposed on a public route. Bound apps connect directly to the app over TCP/5432 under the network policy.

The static demo password is intentionally shared across PostgreSQL instances. This is suitable only for labs and development environments. Change it in `config/services.yml` before deploying in any environment where that default is not acceptable.

## Redis MVP behavior

Redis uses the official `redis:7-alpine` image with password authentication enabled. Its internal `apps.internal` route is portless and is used only for DNS registration; bound apps connect directly to the Silk overlay IP on TCP/6379 under an app-to-app network policy. RDB snapshots and AOF persistence are disabled, so Redis data is ephemeral. The shared demonstration password and binding URI are configured in `config/services.yml`; replace the password before using outside a lab.

## Garage MVP behavior

Garage is configured as an ephemeral, single-node S3-compatible service using the derived `ghcr.io/rkoster/cf-esb-garage:2.3.1` Docker image, based on `dxflrs/garage:v2.3.0`. Its small Go entrypoint writes the Garage TOML config using environment variables, then replaces itself with Garage's `--single-node --default-bucket` server process. The image declares its S3 listener port with `EXPOSE 3900` and is published by `.github/workflows/publish-garage-image.yml` whenever `images/garage/` changes; the workflow tests the image's exposed-port metadata and uses the repository's `GITHUB_TOKEN` to publish to GHCR. CF maps a portless `apps.internal` route to Garage as a DNS registration so BOSH DNS/Service Discovery Controller resolves the hostname to the app's Silk overlay IP. CF Networking's app-to-app policy allows direct container-to-container L3 TCP traffic on port 3900; S3 data traffic does not pass through Gorouter. PostgreSQL and Garage both use portless DNS-registration routes, with each service's listener port opened separately by the corresponding network policy. The broker returns the configured static S3 endpoint, bucket, access key and secret key in each binding.

The Garage metadata and data directories currently live in `/tmp` in the app container; CF restarts, restaging, or replacement erase the object data. All instances use the same configured demonstration credentials and bucket name. This is for labs and demos only, not production. Bound clients use `http://<app-name>.apps.internal:3900` directly over the app-to-app TCP policy. The portless internal route registers the hostname with CF service discovery; Garage's TCP/3900 data connection is direct to the resolved Silk overlay IP.

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
| `CF_INSTANCE_SPACE` | yes | Space where service apps are created |
| `CF_SKIP_TLS_VALIDATION` | no | Set to `true` only for lab foundations with untrusted certificates. Applies to both CAPI and CF Networking calls. |
| `BROKER_USERNAME` | yes for production | HTTP Basic username for OSB requests |
| `BROKER_PASSWORD` | yes for production | HTTP Basic password for OSB requests |
| `SERVICES_CONFIG` | no | Service config path; defaults to `config/services.yml` |
| `PORT` | no | HTTP listen port; defaults to `8080` |

The CF client requires an OAuth client with permissions to create/start/delete apps, manage routes in the configured space, read credential bindings, and create/delete CF Networking policies (`network.write` or `network.admin`, depending on the foundation). Both `CF_API_URL` and `CF_UAA_URL` are supplied explicitly.

### Packaged service YAML

`config/services.yml` is read at broker startup and packaged with the app. It defines catalog metadata, image, port, container environment, plans, fixed binding credentials, and the URI/host template. Adding a service is intended to be a YAML-only operation as long as it follows the shared service schema. Garage's image has no shell, so its runtime config is generated by the derived image entrypoint.

The `environment` values are injected literally into the service app. Keep the corresponding binding credentials in sync with the image’s initialization variables. For PostgreSQL this means `POSTGRES_DB`, `POSTGRES_USER`, and `POSTGRES_PASSWORD` match the credentials returned on bind. For Redis, `REDIS_PASSWORD` must match the password and authenticated URI returned on bind.

## Local development

With Devbox installed:

```sh
devbox shell
go test ./...
go run ./cmd/cf-esb
```

To push the broker and create or update its global Cloud Foundry service-broker registration, run `devbox run deploy`. It also enables marketplace access for configured offerings when access is not already enabled. `CF_SPACE` is the broker app space; `CF_INSTANCE_SPACE` is the separate space used for service apps. The task requires the Cloud Foundry CLI to be logged in and targeted access to the configured spaces, plus the ignored local `.secrets` vars file. Re-running it updates the app and registration and leaves already-enabled access unchanged. Before provisioning Garage, ensure the GHCR package `cf-esb-garage` has been published and is accessible to the foundation's Docker staging component.

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
- Service deprovisioning is asynchronous and likewise requires `accepts_incomplete=true`; poll `last_operation` with the returned operation until deletion succeeds or fails.
- The foundation must provide CF internal app service discovery (for `apps.internal`) to resolve service endpoints, and CF Networking to enforce app-to-app policies.
- CAPI network policy endpoints require a foundation with CF Networking enabled and appropriate policy-server authorization.
