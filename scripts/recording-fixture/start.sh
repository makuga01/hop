#!/bin/sh
set -eu

# This container contains only generated demonstration files. No host mounts,
# published ports, or outbound network are needed. Keys exist only at runtime.
mkdir -p /run/sshd /home/demo/.ssh /home/demo/Projects/exports /home/demo/Projects/scripts
mkdir -p /home/deploy/.ssh /home/deploy/releases /home/deploy/docs
ssh-keygen -q -t ed25519 -N '' -C hop-recording-client -f /home/demo/.ssh/id_ed25519
ssh-keygen -q -t ed25519 -N '' -C hop-recording-server -f /run/ssh_host_ed25519_key
cp /home/demo/.ssh/id_ed25519.pub /home/deploy/.ssh/authorized_keys
awk '{print "[127.0.0.1]:2222 " $1 " " $2}' /run/ssh_host_ed25519_key.pub > /home/demo/.ssh/known_hosts
cat > /home/demo/.ssh/config <<'CONFIG'
Host demo-lab staging-demo backup-demo
    HostName 127.0.0.1
    Port 2222
    User deploy
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
AllowUsers deploy
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
chmod 700 /home/demo/.ssh /home/deploy/.ssh
chmod 600 /home/demo/.ssh/id_ed25519 /home/demo/.ssh/config /home/deploy/.ssh/authorized_keys
exec /usr/sbin/sshd -D -e -f /run/sshd_config
