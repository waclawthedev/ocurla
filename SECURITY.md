# Security and operational risks

Ocurla is experimental software that rewrites requests and invokes curl.
It is not a sandbox, access-control system, or guarantee that credentials and
endpoint details will remain private.

**Following every suggestion in this document does not make use of ocurla
secure or prevent data loss, credential exposure, or other harm.** This document
is not a complete security assessment or a checklist for safe deployment.
Unknown vulnerabilities and risks specific to your environment may remain.

## Examples of known limitations

This list is not exhaustive.

- Requests use the permissions of the supplied credentials. Bugs, configuration
  mistakes, or commands from an AI can send unintended requests, change or delete
  remote data, expose information, or incur API charges. Ocurla does not approve
  destructive operations or provide backups or rollback.
- Stored tokens are plaintext. Config files, inherited environment variables,
  and temporary curl config files may be accessible to processes running as the
  same user. Unix file permissions do not isolate an AI running as that user.
  Windows protection depends on filesystem access controls. Forced termination
  can leave temporary files containing credentials behind.
- Output redaction only replaces matching literal values. Encoded values, trace
  output, IP addresses, and other identifying response data may remain visible.
  Redaction can also alter response content, including binary data. Setting
  `OCURLA_REDACT=0` disables it.
- Files curl writes directly are not redacted. Curl's redirects, proxies,
  credential-forwarding options, and configuration files retain their own
  behavior. Rules do not inspect URLs or headers loaded from curl config files,
  header files, or curl variable expansion. Unmatched URLs pass through.

## Precautions with limited protection

The following measures may reduce some risks. They do not eliminate those risks
or establish that ocurla is suitable for your environment or data.

Test against a disposable environment. Use narrowly scoped, short-lived tokens
and read-only permissions where possible. Keep backups and verify your mapping
before permitting writes. Restrict an AI's access to secrets separately from
ocurla. Avoid committing credentials, private configuration, or sensitive logs.

If credentials may have leaked, revoke or rotate them and review the affected
service's activity logs. These are initial response steps, not a complete
incident-response procedure; they do not undo disclosures or other damage.

## Reporting a vulnerability

Do not include real tokens, private endpoints, personal data, or sensitive logs
in public issues. Use dummy values in reproduction steps.

If GitHub's **Security → Report a vulnerability** option is available for this
repository, use it for sensitive reports. Otherwise, open an issue asking for a
private reporting channel without disclosing the vulnerability details or
secrets. No response time, fix, or security-update schedule is guaranteed.

## License

The [MIT license](LICENSE) contains the warranty disclaimer and limitation of
liability. This document explains practical risks; it does not replace the
license, add use restrictions, or exclude rights or liabilities that applicable
law does not allow to be excluded.
