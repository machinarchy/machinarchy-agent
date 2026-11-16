# Rollback

Agent binaries and controller protocol support are independently released.
Before rolling back an Agent, verify that the older release remains compatible
with the controller and protocol version. Do not roll back across a protocol
change that the controller no longer supports.

## Native Linux binary

The installer retains one previous binary and systemd unit under
`/usr/local/lib/machinarchy-agent/`. To swap back to that version:

```bash
sudo ./rollback-systemd.sh
```

The helper preserves the current version as the next rollback target and does
not modify `/etc/machinarchy-agent`, `/var/lib/machinarchy-agent`, the Agent
identity or the durable operation store. Keep the original verified release
archives as well; the on-host backup contains only one prior version.

For a manual rollback, download and verify the previous release archive, then
run its `install-systemd.sh`. The installer preserves the current binary/unit
as the next rollback target.

## Operational sequence

1. Stop new operation scheduling for the target Agent/canary group in the
   controller.
2. Allow or cancel current operations according to controller policy.
3. Roll back the native binary using the helper or a verified release archive.
4. Verify lease renewal, event subscription and operation-result persistence.
5. Resume scheduling only after controller-side health is confirmed.

Preserve the identity state file unless the controller explicitly revokes the
identity and instructs re-enrollment. Never restore an old identity file from a
backup without understanding lease rotation and revocation consequences.

## Docker

The current container image is not a supported host OS-management deployment.
Container rollback and host systemd integration are outside this native binary
phase and must not be inferred from the native rollback procedure.
