FROM python:3.5-onbuild

RUN apt-get update && apt-get install -y openssh-server netcat
RUN sed -i 's/without-password/yes/' /etc/ssh/sshd_config && \
    echo "ClientAliveInterval 1\nGatewayPorts yes\n" >> /etc/ssh/sshd_config && \
    sh -c 'echo -n "hello\nhello\n" | passwd'

CMD /etc/init.d/ssh start && twistd -n -y ./forwarder.py
