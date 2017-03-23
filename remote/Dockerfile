FROM python:3.5-alpine

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app
COPY requirements.txt /usr/src/app

RUN apk add --no-cache netcat-openbsd openssh gcc libc-dev && \
    ssh-keygen -A && \
    echo -e "ClientAliveInterval 1\nGatewayPorts yes\nPermitRootLogin yes\n" >> /etc/ssh/sshd_config && \
    echo -e "hello\nhello\n" | passwd root && \
    pip install --no-cache-dir -r requirements.txt && \
    apk del -r gcc libc-dev

COPY forwarder.py /usr/src/app
COPY socks.py /usr/src/app
COPY . /usr/src/app

CMD /usr/sbin/sshd -f /etc/ssh/sshd_config && \
    env PYTHONPATH=/usr/src/app twistd -n -y ./forwarder.py
