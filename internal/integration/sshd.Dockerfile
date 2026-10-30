# internal/integration/sshd.Dockerfile —— 最小 sshd 目标机
FROM alpine:3.20
RUN apk add --no-cache openssh \
    && ssh-keygen -A \
    && echo 'root:gengar-it-pass' | chpasswd \
    && sed -i 's/#PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config \
    && sed -i 's/#PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config
EXPOSE 22
CMD ["/usr/sbin/sshd", "-D", "-e"]
