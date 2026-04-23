# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 1.0.0   | :white_check_mark: |

## Reporting a Vulnerability

If you discover a security vulnerability in this project:

1. **Do not** create a public GitHub issue
2. Report via one of the following:
   - **Preferred:** [GitHub Security Advisories](https://github.com/itential/job-metrics-exporter/security/advisories/new) (report privately)
   - **Alternative:** security@itential.com
3. Include in your report:
   - Description of the vulnerability
   - Steps to reproduce
   - Affected versions
   - Impact assessment
   - Suggested fix (if any)

We will acknowledge your report within 48 hours and provide regular updates on our progress toward a fix. We follow coordinated disclosure practices.

## Security Best Practices

- **Credentials:** Never hardcode MongoDB passwords in config files committed to version control. Use environment variables (`ITENTIAL_JOB_METRIC_MONGO_PASSWORD`) or the systemd `EnvironmentFile` to inject secrets at runtime.
- **MongoDB user:** The exporter requires only the `read` role on the `itential` database. Do not grant write access or admin privileges.
- **TLS:** Enable TLS for both the MongoDB connection and the exporter HTTP endpoint in production environments. Set `insecure_skip_verify: false` — never disable certificate verification in production.
- **systemd hardening:** The sample service unit enables `NoNewPrivileges`, `ProtectSystem`, `PrivateTmp`, and other security restrictions. Retain these when deploying.
- **Dependencies:** Keep Go module dependencies up to date. Run `go list -m -u all` to check for available updates and monitor Go security advisories.
