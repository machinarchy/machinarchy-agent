# Changelog

User-visible changes are documented here. Internal AI-assisted implementation
notes and file-level details belong in `ChangeLogAI.md`.

## Unreleased

- Added opt-in Agent command and shell-script execution capabilities using
  shared transport-neutral execution contracts.
- Added bounded stdout/stderr capture and non-zero exit result reporting.

- Extracted the agent runtime into an independent Go module.
- Added standalone build, test, container and systemd deployment scaffolding.
- Moved versioned wire contracts to the separate `machinarchy-protocol` module.
