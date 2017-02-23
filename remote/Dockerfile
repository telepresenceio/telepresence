FROM kylemanna/openvpn

ADD init.sh /tmp/init.sh
RUN ["chmod", "a+x", "/tmp/init.sh"]

# We don't rely on openvpn's encryption for security, really, so pre-generate
# the CA private key and include in the image.
RUN ["/tmp/init.sh"]

ADD entrypoint.sh /usr/local/bin/entrypoint.sh
RUN ["chmod", "a+x", "/usr/local/bin/entrypoint.sh"]
CMD ["/usr/local/bin/entrypoint.sh"]
