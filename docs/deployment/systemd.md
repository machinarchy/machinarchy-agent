# systemd Deployment

Build and install the binary:

```bash
make build
sudo ./scripts/install-systemd.sh
```

Create `/etc/machinarchy-agent/agent.env` with production values. Example:

```bash
MACHINARCHY_ENVIRONMENT=production
MACHINARCHY_LOG_LEVEL=info
MACHINARCHY_AGENT_NATS_URL=tls://nats.example.internal:4222
MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE=/etc/machinarchy-agent/agent.creds
MACHINARCHY_AGENT_ENROLLMENT_TOKEN=replace-with-one-time-token
MACHINARCHY_AGENT_STATE_FILE=/var/lib/machinarchy-agent/identity.json
```

Protect the files:

```bash
sudo chown root:machagent /etc/machinarchy-agent/agent.env
sudo chmod 0640 /etc/machinarchy-agent/agent.env
sudo chown machagent:machagent /etc/machinarchy-agent/agent.creds
sudo chmod 0600 /etc/machinarchy-agent/agent.creds
```

Start and inspect:

```bash
sudo systemctl enable --now machinarchy-agent
sudo systemctl status machinarchy-agent
sudo journalctl -u machinarchy-agent -f
```

After successful enrollment, remove the one-time enrollment token from the
environment file and restart the service. The controller-issued identity and
lease token remain in the protected state file.
