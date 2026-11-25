## Summary

Describe the change and its purpose.

## Scope

- [ ] Agent-only change
- [ ] Shared protocol change (protocol repository PR linked)
- [ ] Controller integration change (controller repository PR linked)

## Safety and reliability

- [ ] Inputs and outputs are bounded
- [ ] Context cancellation and timeouts are handled
- [ ] No secret values are logged
- [ ] No unrestricted shell execution is introduced
- [ ] Protocol compatibility is preserved or explicitly versioned

## Verification

- [ ] `make fmt-check`
- [ ] `make test`
- [ ] `make test-race`
- [ ] `make vet`
- [ ] `make build`
- [ ] Real NATS/controller integration tests (when relevant)

## Documentation

- [ ] `ChangeLogAI.md` updated
- [ ] `CHANGELOG.md` updated when user-visible behavior changes
- [ ] Relevant docs and examples updated
