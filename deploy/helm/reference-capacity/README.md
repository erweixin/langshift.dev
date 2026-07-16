# Reference capacity controller chart

This chart deploys the fixed Stage 3 load controller outside the system-under-test. It is intentionally a single `Recreate` replica because run state is an attestation boundary, not an HA product API. A failed controller invalidates the run and the observer starts a fresh environment.

The dataset PVC is prepared by the reference-environment seeder and is mounted read-only. It contains `dataset.json` plus every short-lived session and CSRF credential file referenced by that manifest. The manifest is bound to the exact source commit and its SHA-256 must equal `environment.datasetManifestSha256` in the observer configuration. Never store the credential files in Git, a ConfigMap, the report, or a metrics label.

The Secret named by `existingSecret` must contain `controller-token`, `server-cert.pem`, `server-key.pem`, `observer-ca.pem`, `gateway-ca.pem`, `gateway-client-cert.pem`, `gateway-client-key.pem`, `otel-ca.pem`, and `otel-token`. The controller namespace should use dedicated load-generator nodes; its CPU, memory and network utilization are not part of the product capacity result.
