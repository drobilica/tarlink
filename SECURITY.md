# Security policy

TarLink treats release archives, registry metadata, URLs, and local filesystem contents as hostile input. Please do not disclose a suspected vulnerability in a public issue before maintainers have had a chance to investigate.

## Reporting

Use GitHub's private security advisory flow for this repository when available. If it is unavailable, contact the maintainers privately through the address listed in the repository's GitHub security settings. Include:

- affected commit, release, or package;
- a concise impact statement;
- reproduction steps or a minimal fixture;
- any required platform, permissions, or network conditions.

Do not include real credentials, private registry URLs, or user data in a report.

## Scope

Reports involving archive extraction, path/link traversal, digest or HTTPS bypasses, registry generation validation, ownership validation, purge deletion, atomic activation, state corruption, privilege escalation, or denial of service are high priority. TarLink does not sandbox installed applications; bugs in an application after activation are outside TarLink's boundary unless TarLink caused the unsafe execution.

The project supports rootless Linux amd64 and arm64 clients. Application availability is an exact manifest-platform decision: the client uses its `GOOS`/`GOARCH` pair and never falls back to another variant. A report requiring root or a system service is not representative of the supported deployment model.

## Security guarantees

See [docs/security-model.md](docs/security-model.md) and [docs/threat-model.md](docs/threat-model.md) for the enforceable limits and assumptions. Security-sensitive changes require focused tests, an explanation of the trust boundary, and review of rollback and failure behavior.
