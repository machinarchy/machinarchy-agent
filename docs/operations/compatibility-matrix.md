# Controller / Agent Compatibility Matrix

Maintain this table for every release. Do not assume arbitrary versions are
compatible merely because both compile.

| Controller release | Agent release | Protocol module | Status | Notes |
|---|---|---|---|---|
| Not yet verified | Not yet released | `v0.1.0` | Not production-approved | Contract is provisional; real end-to-end verification pending |

Update this file only after running the controller -> agent -> controller
integration suite against the tagged releases and the actual NATS ACL model.
