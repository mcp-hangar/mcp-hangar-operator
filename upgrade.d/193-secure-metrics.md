### `/metrics` needs HTTPS and an authorized bearer token

The operator's metrics endpoint (`--metrics-bind-address`, `:8080` by default)
now serves HTTPS with a self-signed certificate generated in memory, and
refuses a request without a bearer token the API server accepts and authorizes
for `get` on the `/metrics` non-resource URL. A scraper still using plain HTTP,
or no token, gets nothing; the metrics are not lost, only unread until the
scrape config changes.

To keep scraping:

- Scrape `https`, skip certificate verification (or pin the certificate), and
  send the scraper's ServiceAccount token. With the Prometheus Operator:
  `scheme: https`, `bearerTokenFile:
  /var/run/secrets/kubernetes.io/serviceaccount/token` (or `authorization`),
  `tlsConfig.insecureSkipVerify: true`.
- Bind the scraper's ServiceAccount to a ClusterRole allowing `get` on the
  `/metrics` non-resource URL. `config/rbac/metrics_reader_role.yaml` ships one
  named `metrics-reader`.
- The operator itself now needs `create` on `tokenreviews` and
  `subjectaccessreviews`; `config/rbac/role.yaml` carries it. A chart that does
  not grant it leaves every scrape refused.

To keep the old behaviour, start the operator with `--metrics-secure=false`.
