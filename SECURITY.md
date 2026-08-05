# Security

## Reporting a vulnerability

Report security issues privately by opening a GitHub Security Advisory on this repository, or by contacting the maintainer directly via the information listed in [MAINTAINERS](MAINTAINERS).

Do not open a public issue for security vulnerabilities.

## Scope

The following are considered in scope:

- Remote code execution via probe endpoints (ping, traceroute, dig, portcheck)
- Authentication bypass on agent endpoints
- Information disclosure of internal network topology via error messages
- Rate limiting bypass
- Path traversal or arbitrary file read via the Permanent Link report ID

## Out of scope

- Denial of service via legitimate high-volume usage (covered by rate limiting configuration)
- Security of the underlying OS or network infrastructure

## Response

Security reports will be acknowledged within 72 hours. A fix will be issued as soon as practicable. The reporter will be credited in the CHANGELOG unless they request otherwise.