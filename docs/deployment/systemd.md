# Native Linux binary deployment (systemd)

The supported native deployment is a Linux AMD64 or ARM64 binary managed by
systemd. The release archive contains the binary, systemd unit, environment
file template, installer, uninstaller, rollback helper and this guide.

## 1. Download and verify a release

Choose the release tag and architecture (`amd64` or `arm64`):

```bash
VERSION=v0.1.0
ARCH=amd64 # use arm64 on 64-bit ARM Linux
ASSET="machinarchy-agent_${VERSION}_linux_${ARCH}.tar.gz"
BASE="https://github.com/machinarchy/machinarchy-agent/releases/download/${VERSION}"

curl --fail --location --proto '=https' --tlsv1.2 --output "${ASSET}" "${BASE}/${ASSET}"
curl --fail --location --proto '=https' --tlsv1.2 --output checksums.txt "${BASE}/checksums.txt"
grep -F "  ${ASSET}" checksums.txt | sha256sum --check

tar -xzf "${ASSET}"
cd "machinarchy-agent_${VERSION}_linux_${ARCH}"
```

The checksum detects accidental corruption or an incomplete download. Because
the archive and checksum file are hosted by the same release, this alone is not
a cryptographic publisher signature. Obtain release assets only from the
Machinarchy Agent GitHub repository over HTTPS.

## 2. Install or upgrade

Run the installer from the extracted release directory:

```bash
sudo ./install-systemd.sh
```

The installer:

- creates a dedicated `machagent` system user/group without forcing a numeric
  UID or GID;
- installs the binary and unit file using atomic file replacement;
- creates `/etc/machinarchy-agent` as `root:machagent` mode `0750`;
- creates `/etc/machinarchy-agent/agent.env` from the shipped template only if
  the file does not already exist;
- preserves the existing environment file, credentials, identity and operation
  state across upgrades;
- keeps the previous binary and unit for one-step rollback;
- restarts the service only when it was already active before the upgrade.

You can also provide explicit source paths when installing a locally built
binary:

```bash
make build
sudo ./scripts/install-systemd.sh \
  --binary ./bin/machinarchy-agent \
  --service ./deployments/systemd/machinarchy-agent.service
```

## 3. Configure the Agent

Edit the production environment file:

```bash
sudoedit /etc/machinarchy-agent/agent.env
```

At minimum, configure a TLS NATS endpoint, one authentication method, the
controller's public key, and the one-time enrollment token. See
[configuration.md](../configuration.md) for every setting. For example:

```bash
MACHINARCHY_AGENT_NATS_URL=tls://nats.example.internal:4222
MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE=/etc/machinarchy-agent/agent.creds
MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY_FILE=/etc/machinarchy-agent/controller.pub
MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE=/etc/machinarchy-agent/enrollment.token
```

The unit already sets `MACHINARCHY_ENVIRONMENT=production` and
`MACHINARCHY_AGENT_STATE_FILE=/var/lib/machinarchy-agent/state.json`.

Protect credentials so only the Agent account can read them:

```bash
sudo chown root:machagent /etc/machinarchy-agent/agent.env
sudo chmod 0640 /etc/machinarchy-agent/agent.env
sudo chown machagent:machagent /etc/machinarchy-agent/agent.creds /etc/machinarchy-agent/enrollment.token
sudo chmod 0600 /etc/machinarchy-agent/agent.creds /etc/machinarchy-agent/enrollment.token
```

The installer deliberately does not enable or start the service. Validate the
configuration using a transient systemd unit so systemd reads the same
`EnvironmentFile` syntax and the check runs as the unprivileged Agent account:

```bash
sudo systemd-run --wait --pipe --collect \
  --unit=machinarchy-agent-config-check \
  --uid=machagent --gid=machagent \
  --property=EnvironmentFile=/etc/machinarchy-agent/agent.env \
  /usr/local/bin/machinarchy-agent --check-config
```

The check validates configuration without opening a broker connection or
starting the Agent runtime. It does not print secret values. The `--collect`
option requires systemd 236 or newer; on older versions, omit `--collect` and
use a fresh `--unit` name for each attempt. After the check succeeds, enable and
start the service:

```bash
sudo systemctl enable --now machinarchy-agent
sudo systemctl status machinarchy-agent
sudo journalctl -u machinarchy-agent -f
```

After the log shows `agent registered`, delete the enrollment token file and
its setting. The agent key and the controller-assigned identity remain in the
protected state file (`/var/lib/machinarchy-agent/state.json`). If the
controller revokes the agent, the agent deletes its identity and exits; it then
needs a new enrollment token.

## 4. Upgrade and rollback

Use the new version's release archive and run its installer again:

```bash
sudo ./install-systemd.sh
```

If the service was active, the installer restarts it. The previous binary and
unit are saved under `/usr/local/lib/machinarchy-agent/`.

To restore the previous installation:

```bash
sudo ./rollback-systemd.sh
```

The rollback helper swaps the current and previous binaries/units so the
previous version remains available as the next rollback target. It preserves
Agent identity and durable operation state. Before a rollback, verify that the
older Agent protocol version remains compatible with the controller.

Only one previous version is retained. This is a convenience rollback, not a
substitute for keeping verified release archives.

## 5. Uninstall

The default uninstall removes the service unit, executable and rollback copy,
but deliberately preserves configuration, credentials, identity and
operation state:

```bash
sudo ./uninstall-systemd.sh
```

To permanently remove configuration and Agent state as well, use the explicit
purge option. The script asks for confirmation:

```bash
sudo ./uninstall-systemd.sh --purge
```

For non-interactive automation, acknowledge the destructive operation
explicitly:

```bash
sudo ./uninstall-systemd.sh --purge --yes
```

Do not purge state while operations are pending or when you may need to
reinstall the Agent with its existing controller identity.

## 6. Service management privileges

The Agent runs as the unprivileged `machagent` account. It does not use `sudo`
or automatically elevate privileges. OS service control remains disabled by
default and is subject to the host's local systemd/Polkit policy. Configure an
exact service allowlist and a narrowly scoped host privilege policy before
enabling it. Never grant broad service-control permissions simply to make the
Agent work.

## 7. Supported platforms

| OS | Architecture | Deployment | Status |
|---|---|---|---|
| Linux with systemd | x86-64 (`amd64`) | Native binary | Release target |
| Linux with systemd | 64-bit ARM (`arm64`) | Native binary | Release target |
| Linux without systemd | amd64/arm64 | Native binary | Not supported by this installer |
| Windows/macOS | Any | Native binary | Not supported |
