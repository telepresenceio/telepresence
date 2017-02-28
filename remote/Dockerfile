FROM python:3.5-onbuild

RUN apt-get update && apt-get install -y openssh-server netcat && \
    sed -i 's/without-password/yes/' /etc/ssh/sshd_config && \
    sh -c 'echo -n "hello\nhello\n" | passwd'

CMD /etc/init.d/ssh start && twistd -n -y ./forwarder.py
