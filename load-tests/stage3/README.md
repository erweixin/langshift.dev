# Stage 3 reference-capacity gate

This gate accepts evidence only from the complete reference production topology. The local foundation Compose file is deliberately ineligible.

The load controller must sustain every vector in `reference-capacity-profile.json` concurrently for at least 1,800 seconds and export a single JSON result. Every measurement must include its Prometheus query, observation count and raw-series SHA-256. The result must also bind the source commit, deployment manifest hash, image digests, hardware/region topology, provider emulator version, runtime image digest, dataset size and wall-clock start/end timestamps.

Run `npm run gate:stage3:capacity -- /absolute/path/to/raw-capacity-result.json`. The verifier rejects missing vectors, scaled-down profiles, synthetic/local topology, insufficient duration, SLO violations, safety invariant violations, absent raw evidence hashes and source-commit drift. A passing report is written only after all checks succeed.

`npm run gate:stage3:capacity:self-test` exercises the verifier with an in-memory compliant result and one mutation for every threshold. It is a verifier test, not capacity evidence.
