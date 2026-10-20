# Test machine for the Docker end-to-end suite (cmd/enigma/docker_e2e_test.go):
# a Linux box with sshd and a user "dev". The enigma binary and the ssh key
# are mounted at run time, so the image never needs rebuilding for code
# changes.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server openssh-client procps ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd \
 && ssh-keygen -A \
 && useradd -m -s /bin/sh dev \
 && mkdir -p /home/dev/.ssh \
 && printf 'Host *\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel ERROR\n  IdentityFile /home/dev/.ssh/id\n' > /home/dev/.ssh/config \
 && chown -R dev:dev /home/dev/.ssh && chmod 700 /home/dev/.ssh \
 && sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && echo 'AuthorizedKeysFile /keys/authorized_keys' >> /etc/ssh/sshd_config \
 && echo 'StrictModes no' >> /etc/ssh/sshd_config
EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
