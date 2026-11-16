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

Verify the Agent is registered, its lease is valid, JetStream is enabled,
the configured stream exists, and the controller provisioned the durable
consumer `agent_<agent-id>` with the exact filter
`machinarchy.v1.agent.<agent-id>.events`. The Agent only binds to this consumer;
it does not create it. Verify the Agent credentials can pull and acknowledge
only this consumer and cannot access other Agents' subjects.

## Results not visible in controller

Verify the Agent can publish to its own result subject and the controller's
result subscriber authenticates the Agent identity, validates and durably
persists the result, then publishes a terminal result acknowledgement through
the Agent's JetStream command subject. A successful Core NATS flush means the
server received the result publish; it does not prove controller persistence.

## Identity state permissions

The state file should be mode `0600`; its parent directory should be `0700`.
The service account must own both. Do not delete the state file casually: a
new enrollment attempt may create a second identity unless the controller
explicitly revokes the previous one.

## Agent fails to start while recovering pending operations

If startup reports that the durable accepted-operation backlog exceeds the
configured queue capacity, restore the previous
`MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE` value or increase it to at least the
reported backlog size, then restart the Agent. Do not delete the operation
store to bypass this check: it contains deduplication state and pending work.
