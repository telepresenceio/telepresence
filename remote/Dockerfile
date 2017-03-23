FROM python:3.5-slim

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app
COPY requirements.txt /usr/src/app

RUN apt-get update && \
    apt-get install --no-install-recommends -y openssh-server netcat && \
    apt-get install --no-install-recommends -y gcc && \
    pip install --no-cache-dir -r requirements.txt && \
    apt-get remove gcc && \
    apt-get clean

RUN sed -i 's/without-password/yes/' /etc/ssh/sshd_config && \
    echo "ClientAliveInterval 1\nGatewayPorts yes\n" >> /etc/ssh/sshd_config && \
    sh -c 'echo -n "hello\nhello\n" | passwd'

COPY forwarder.py /usr/src/app
COPY socks.py /usr/src/app
COPY . /usr/src/app

CMD /etc/init.d/ssh start && \
    env PYTHONPATH=/usr/src/app twistd -n -y ./forwarder.py
