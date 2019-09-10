# choose base os: (no other change should be required besides FROM)
FROM centos:centos7
#FROM rhel7

# when there is a proxy, build this file like this:
# V=0.101; docker build -f Dockerfile.ocp -t registry.corp.example.com/telepresence/telepresence-k8s:$V  --build-arg  http_proxy="http://proxy.corp.example.com:8080" --build-arg https_proxy="http://proxy.corp.example.com:8080" --build-arg no_proxy=".example.com" . ; docker push registry.corp.example.com/telepresence/telepresence-k8s:$V

# Work is based on: https://github.com/openshift-qe/ssh-git-docker/blob/master/ssh-git-openshift/Dockerfile
# and on https://github.com/telepresenceio/telepresence/blob/master/k8s-proxy/Dockerfile

# ed - is requried for editing passwd file
# centos-release-scl - is required for python3 in centos (same way rhel-server-rhscl-7-rpms repo is requried for rhel)
RUN yum -y install openssh-server ed centos-release-scl && \
    yum clean all && \
    rm -rf /var/cache/yum

RUN echo -e "ClientAliveInterval 1\nGatewayPorts yes\nPermitEmptyPasswords yes\nPort 8022\nClientAliveCountMax 10\nPermitRootLogin yes\n" >> /etc/ssh/sshd_config
RUN chmod 775 /var/run
RUN rm -f /var/run/nologin

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app

COPY requirements.txt /usr/src/app

# Install scl and python3
RUN yum-config-manager --enable centos-sclo-rh || true #Centos: centos-sclo-rh or centos-sclo-sclo
RUN yum-config-manager --enable rhel-server-rhscl-7-rpms || true #RHEL: rhel-server-rhscl-7-rpms
RUN yum -y install scl-utils-build rh-python36 rh-python36-python-setuptools && \
    yum clean all && \
    rm -rf /var/cache/yum

# Install required python3 modules
RUN yum -y install gcc && \
    source scl_source enable rh-python36 && \
    pip3 install --no-cache-dir incremental && \
    pip3 install --no-cache-dir -r requirements.txt && \
    yum -y remove gcc && \
    yum clean all && \
    rm -rf /var/cache/yum

# Not a must; sets python3 when running exec inside container using bash shell
RUN echo -e '#!/bin/bash\nsource scl_source enable rh-python36' >/etc/profile.d/scl_enable.sh

# setup telepresence user
RUN adduser --system -s /bin/bash -u 1000 -g 0 --home-dir /usr/src/app/ telepresence
# in ocp, uid 1000 might get replaced by a different value at runtime -> see CMD below

# telepresence user should be passwordless
RUN passwd -d telepresence

# Best practices: as per https://docs.okd.io/latest/creating_images/guidelines.html#openshift-specific-guidelines
RUN chgrp -R 0 /usr/src/app /etc/ssh /etc/passwd /etc/group && \
    chmod -R g=u /usr/src/app /etc/ssh /etc/passwd /etc/group

# copy telepresence files
COPY forwarder.py /usr/src/app
COPY socks.py /usr/src/app
COPY . /usr/src/app

EXPOSE 8022
USER 1000:0
CMD echo -e ",s/1000/`id -u`/g\\012 w" | ed -s /etc/passwd && \
    ssh-keygen -A && \
    source scl_source enable rh-python36 && \
    /usr/src/app/pre-run.sh

