# Security policy

## Reporting a vulnerability

Use [GitHub private vulnerability reporting](https://github.com/erweixin/langshift.dev/security/advisories/new). Do not open a public issue, discussion or pull request with exploitable details, customer data, credentials or a working secret.

Include the affected commit or image digest, deployment mode, prerequisites, reproduction steps, impact, tenant or privilege boundary crossed, and any suggested mitigation. Minimize personal data and use synthetic accounts. If an attachment contains active payloads, label and encrypt it using a channel agreed inside the private advisory.

## Response targets

- Critical: acknowledge within 1 business day, begin containment immediately, and provide status at least daily.
- High: acknowledge within 2 business days and provide status at least every 3 business days.
- Medium/Low: acknowledge within 5 business days and schedule according to risk.

These are response targets, not a promise that every report is valid or fixed within a fixed period. Coordinated disclosure timing is agreed in the private advisory after affected users can be protected. The project may request a CVE through GitHub Security Advisories when appropriate.

## Scope and safe research

In scope are Lites source, official signed images and documented deployment assets. Test only accounts, tenants and infrastructure you own or are explicitly authorized to assess. Stop if you encounter another person's data, avoid persistence, denial of service, social engineering, spam, physical attacks and destructive data modification, and delete retained test data after coordination.

Good-faith research that follows this policy will not be intentionally pursued as a violation of the project's access policies. This statement cannot authorize testing of third-party systems or waive rights belonging to others. There is no public bug-bounty or reward promise unless a separate signed program says otherwise.

Supported security fixes are published for the current signed GA release line. Mutable tags and unverified forks are not release identities; always provide the source commit and image digest when reporting.
