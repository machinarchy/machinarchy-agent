# Rollback

Agent binaries and controller protocol support are independently released.
Before rolling back an Agent, verify the target version is compatible with the
controller and protocol version. Do not roll back across a protocol change that
the controller no longer supports.

1. Stop new operation scheduling for the target agent/canary group in the
   controller.
2. Allow or cancel current operations according to controller policy.
3. Stop the systemd service or container.
4. Install the previous verified binary/image.
5. Preserve the identity state file unless the controller explicitly revokes
   the identity and instructs re-enrollment.
6. Start the previous Agent version and verify lease renewal and event
   subscription.
7. Resume scheduling only after controller-side health and result persistence
   are confirmed.

Never restore an old identity file from a backup without understanding lease
rotation and revocation consequences.
