# Security and privacy

CodexPool manages local authentication state. Treat its private data directory as sensitive. See the [security model](docs/security-model.md) for boundaries and the [Desktop guide](docs/desktop.md) for the helper that modifies default authentication state.

## Reporting a vulnerability

If the repository has GitHub private vulnerability reporting enabled, use **Security → Report a vulnerability**. Do not publish exploit details or credentials in public issues. If private reporting is unavailable, open a minimal issue asking maintainers to enable a private reporting channel, without sensitive details.

Use synthetic accounts and redacted paths in reports. Never include authentication files, OAuth URLs/codes, tokens, registry files, private backups, account emails, or raw usage snapshots. Diagnostic JSON can contain local paths and account metadata.

The current release is a preview. Security fixes are intended for the latest preview; no long-term support or response-time guarantee is established.
