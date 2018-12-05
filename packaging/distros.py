"""
List of Linux distributions that get packaging

https://github.com/alanfranz/fpm-within-docker/blob/master/distributions.yml

https://wiki.ubuntu.com/Releases

https://www.debian.org/releases/

https://fedoraproject.org/wiki/Releases
https://fedoraproject.org/wiki/End_of_life
"""

ubuntu_deps = ["torsocks", "python3", "openssh-client", "sshfs", "conntrack"]

install_deb = """
    apt-get -qq update
    dpkg --unpack {} > /dev/null
    apt-get -qq -f install > /dev/null
"""

fedora_deps = [
    "python3", "torsocks", "openssh-clients", "sshfs", "conntrack-tools"
]

install_rpm = """
    dnf -qy install {}
"""

distros = [
    ("ubuntu", "xenial", "deb", ubuntu_deps, install_deb),
    ("ubuntu", "artful", "deb", ubuntu_deps, install_deb),
    (
        "ubuntu", "bionic", "deb", ubuntu_deps + ["python3-distutils"],
        install_deb
    ),
    ("debian", "stretch", "deb", ubuntu_deps, install_deb),
    ("fedora", "26", "rpm", fedora_deps, install_rpm),
    ("fedora", "27", "rpm", fedora_deps, install_rpm),
    ("fedora", "28", "rpm", fedora_deps, install_rpm),
]

# Ubuntu: above plus yakkety zesty bionic
# y and z are EOL, but ...
# Fedora: above plus 28
# 26 is EOL, but ...
# Consider adding easy Centos/RHEL, Debian,
# other stuff on PackageCloud
