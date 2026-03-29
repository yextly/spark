# Security Policy

## Supported Versions

Only the latest released version of the Spark Worker Operator receives security updates.

## Reporting a Vulnerability

If you discover a security issue, please report it privately:

- Do not open a public GitHub issue.
- Contact the maintainers via the project's designated security contact channel.

Please include:

- Description of the vulnerability
- Steps to reproduce (if applicable)
- Affected versions
- Suggested fixes (optional)

You will receive acknowledgment within 72 hours.

## Security Expectations

- Do not exploit discovered vulnerabilities.
- Do not test attacks against production clusters without authorization.
- Report issues responsibly and confidentially.

## Handling of Secrets

This operator dynamically remaps secrets per WorkerInstance. Users should:

- Avoid sharing sensitive secrets across environments
- Validate secret definitions before applying
- Use RBAC to restrict access to WorkerInstances and WorkTemplates

## Disclosure Policy

Once a vulnerability is confirmed and mitigations are available, a public advisory may be released.
