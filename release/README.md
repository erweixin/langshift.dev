# GA release evidence

The GA gate is fail-closed. `scripts/write-stage6-ga-readiness-report.mjs` accepts only evidence bound to the exact signed Release Candidate hash; a screenshot, mutable image tag, local test result, or unsigned statement cannot satisfy a check.

The main supply-chain workflow creates `ga-release-candidate.json`, `product-behavior-manifest.json`, and a Sigstore bundle after every required OCI image has been built with SBOM/provenance and its digest signature has been verified. Copy that immutable artifact to `release-candidates/current/` only for the RC under evaluation.

External and environment-specific evidence is stored under `gate-reports/stage-6/`. Pilot approval remains under `pilot-reports/approved.json`, and its exact behavior comparison is generated with `scripts/compare-product-behavior-manifests.mjs`. The readiness report deliberately remains `pending` until independent penetration testing, profile evals, capacity, recovery, deletion, three deployment matrices, issue inventory, and final approval are present and pass their fixed thresholds.
