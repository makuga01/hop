#!/bin/sh
set -eu

# This container contains only generated demonstration files. No host mounts,
# published ports, or outbound network are needed. Keys exist only at runtime.
mkdir -p /run/sshd /home/demo/.ssh /home/demo/Projects/exports /home/demo/Projects/scripts
mkdir -p /home/deploy/.ssh /home/deploy/releases /home/deploy/docs
mkdir -p /home/developer/.ssh /home/archivist/.ssh
ssh-keygen -q -t ed25519 -N '' -C hop-recording-client -f /home/demo/.ssh/id_ed25519
ssh-keygen -q -t ed25519 -N '' -C hop-recording-server -f /run/ssh_host_ed25519_key
cp /home/demo/.ssh/id_ed25519.pub /home/deploy/.ssh/authorized_keys
cp /home/demo/.ssh/id_ed25519.pub /home/developer/.ssh/authorized_keys
cp /home/demo/.ssh/id_ed25519.pub /home/archivist/.ssh/authorized_keys
awk '{print "[lab.demo]:2222,[staging.demo]:2222,[backup.demo]:2222 " $1 " " $2}' /run/ssh_host_ed25519_key.pub > /home/demo/.ssh/known_hosts
printf '\n127.0.0.1 lab.demo staging.demo backup.demo\n' >> /etc/hosts
cat > /home/demo/.ssh/config <<'CONFIG'
Host demo-lab
    HostName lab.demo
    User developer
Host staging-demo
    HostName staging.demo
    User deploy
Host backup-demo
    HostName backup.demo
    User archivist
Host *
    Port 2222
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes
    StrictHostKeyChecking yes
    LogLevel ERROR
CONFIG
cat > /run/sshd_config <<'CONFIG'
Port 2222
ListenAddress 127.0.0.1
HostKey /run/ssh_host_ed25519_key
PidFile /run/sshd.pid
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
AllowUsers deploy developer archivist
UsePAM no
PrintMotd no
Subsystem sftp internal-sftp
CONFIG
printf 'name,total\nSample North,42\nSample South,18\n' > /home/demo/Projects/report.csv
printf 'environment: demonstration\n' > /home/demo/Projects/config.yaml
printf 'Synthetic build artifact for the Hop recording.\n' > /home/demo/Projects/release.bin
truncate -s 64M /home/demo/Projects/release.bin
printf 'All files on this server are demonstration fixtures.\n' > /home/deploy/welcome.txt
chown -R demo:demo /home/demo
chown -R deploy:deploy /home/deploy
chown -R developer:developer /home/developer
chown -R archivist:archivist /home/archivist
chmod 700 /home/demo/.ssh /home/deploy/.ssh /home/developer/.ssh /home/archivist/.ssh
chmod 600 /home/demo/.ssh/id_ed25519 /home/demo/.ssh/config /home/deploy/.ssh/authorized_keys /home/developer/.ssh/authorized_keys /home/archivist/.ssh/authorized_keys
exec /usr/sbin/sshd -D -e -f /run/sshd_config
