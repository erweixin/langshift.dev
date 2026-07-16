# Product content releases

This directory contains immutable product-content release candidates for the
Lites growth product. A release is a complete unit: career roles, capability
requirements, transition templates, task templates, practice rubrics, source
provenance, and the content update policy move together.

The release is bilingual by construction. English and Simplified Chinese are
properties of the same semantic object rather than independently activated
documents. Capability levels are evidence states, never percentages.

## Build and verify

```sh
npm run gate:stage4:content
```

The build is deterministic and recreates `releases/1.0.0`. The verifier checks
the manifest root, every file digest and byte length, closed references,
bilingual parity, evidence-upgrade boundaries, all 30 transition groups, the
600-sample end-to-end eval dataset, and all five 200-sample Profile slices.
It writes the technical evidence to
`gate-reports/stage-4/content-release-report.json`.

Production processes must load a release through
`internal/product/contentcatalog`. The loader rejects relative paths,
symlinks, unknown JSON fields, digest or size drift, missing translations,
broken references, and task templates that allow automatic capability
upgrades.

## Activation and rollback

`manifest.json` deliberately says `release_candidate`. Content is activated
only through Behavior Control Plane after the product and Profile eval gates
and the accountable approvals pass. The activation binds the complete
`contentRootSha256`; it never copies individual files into an existing release.

Rollback selects a previously approved complete manifest. Files inside an
activated release are never edited in place. Labor-market, accreditation, or
hiring-outcome claims are outside this editorial catalog and require their own
reviewed source release.
