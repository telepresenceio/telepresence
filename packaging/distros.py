"""
List of Linux distributions that get packaging

https://github.com/alanfranz/fpm-within-docker/blob/master/distributions.yml

https://wiki.ubuntu.com/Releases

https://fedoraproject.org/wiki/Releases
https://fedoraproject.org/wiki/End_of_life
"""

ubuntu_deps = [
    "torsocks", "python3", "openssh-client", "sshfs", "socat", "conntrack"
]

install_deb = """
    apt-get -qq update
    dpkg --unpack --recursive /packages > /dev/null
    apt-get -qq -f install > /dev/null
"""

fedora_deps = [
    "python3", "torsocks", "openssh-clients", "sshfs", "socat",
    "conntrack-tools"
]

install_rpm = """
    dnf -qy install /packages/*.rpm
"""

distros = [
    ("ubuntu", "xenial", "deb", ubuntu_deps, install_deb),
    ("ubuntu", "artful", "deb", ubuntu_deps, install_deb),
    ("fedora", "26", "rpm", fedora_deps, install_rpm),
    ("fedora", "27", "rpm", fedora_deps, install_rpm),
]
