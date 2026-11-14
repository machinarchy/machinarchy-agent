# Troubleshooting

## Agent cannot connect to NATS

- Verify DNS and outbound connectivity to the configured NATS endpoints.
- Verify `tls://` is used in production and the host certificate is valid.
- Verify exactly one supported authentication method is configured.
- Verify the credentials file is a regular file, mode `0600`, readable by the
  agent service account.
- Do not paste credentials or tokens into issue reports.

## Registration request times out

A live NATS server is not enough. The controller must subscribe to the
registration request subject, validate the one-time enrollment token, create or
reuse an idempotent agent record, and reply with a valid response envelope.

## Lease renewal rejected

Check whether the agent has been revoked, whether the lease expired, and
whether the controller and agent use compatible protocol versions. Do not
manually edit the identity state file to extend a lease.

## No operation events received

Verify the agent is registered, lease is valid, the controller publishes to
the agent-specific subject, and NATS ACLs permit this agent to subscribe only
to its assigned subject. Core NATS does not replay messages published while
the agent is offline.

## Results not visible in controller

Verify the agent can publish to its own result subject and the controller's
result consumer validates and persists the result. A successful NATS flush
means the server received the publish; it does not prove the controller
persisted the result.

## Identity state permissions

The state file should be mode `0600`; its parent directory should be `0700`.
The service account must own both. Do not delete the state file casually: a
new enrollment attempt may create a second identity unless the controller
explicitly revokes the previous one.
