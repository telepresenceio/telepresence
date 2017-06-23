FROM alpine:3.5

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app
COPY requirements.txt /usr/src/app

# We need openssh for the sftp process, used by sshfs, but for actual sshd we
# use dropbear since it's a lot more forgiving about permissions and the like.
# For some reason pip doesn't install incremental (a Twisted dependency) so do
# so manually. When done, remove unneeded packages for a smaller image.
RUN apk add --no-cache python3 python3-dev openssh gcc libc-dev dropbear && \
    pip3 install --no-cache-dir incremental && \
    pip3 install --no-cache-dir -r requirements.txt && \
    apk del -r gcc libc-dev python3-dev

# Set the permissions necessary to run as a non-root user
RUN mkdir /etc/dropbear
RUN chmod -R g+rwx /etc/dropbear && \
    chmod g+w /etc/passwd && \
    chmod -R g+w /usr/src/app

COPY forwarder.py /usr/src/app
COPY socks.py /usr/src/app
COPY . /usr/src/app

# Running as root will now fail due with a permissions error, so default to some
# other UID
USER 1000

# This is set on the remote pod, both for information purposes but also for use
# by some of the end-to-end tests. Don't delete this without modifying
# corresponding test:
ENV TELEPRESENCE_PROXY=1

CMD /usr/src/app/run.sh
