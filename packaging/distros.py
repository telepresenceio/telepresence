"""
List of Linux distributions that get packaging

https://wiki.ubuntu.com/Releases

https://www.debian.org/releases/

https://fedoraproject.org/wiki/Releases
https://fedoraproject.org/wiki/End_of_life
"""

ubuntu_deps = ["torsocks", "python3", "openssh-client", "sshfs", "conntrack"]
ubuntu_deps_2 = ubuntu_deps + ["python3-distutils"]

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
    ("ubuntu", "xenial", "deb", ubuntu_deps, install_deb),  # 16.04
    ("ubuntu", "artful", "deb", ubuntu_deps, install_deb),  # 17.10 EOL
    ("ubuntu", "bionic", "deb", ubuntu_deps_2, install_deb),  # 18.04
    ("ubuntu", "cosmic", "deb", ubuntu_deps_2, install_deb),  # 18.10
    ("ubuntu", "disco", "deb", ubuntu_deps_2, install_deb),  # 19.04
    ("debian", "stretch", "deb", ubuntu_deps, install_deb),  # stable
    ("fedora", "26", "rpm", fedora_deps, install_rpm),  # EOL 2018-05-29
    ("fedora", "27", "rpm", fedora_deps, install_rpm),  # EOL 2018-11-30
    ("fedora", "28", "rpm", fedora_deps, install_rpm),
    ("fedora", "29", "rpm", fedora_deps, install_rpm),
]
