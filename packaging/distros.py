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
    ("ubuntu", "xenial", "deb", ubuntu_deps, install_deb),  # 16.04 (LTS2021)
    ("ubuntu", "bionic", "deb", ubuntu_deps_2, install_deb),  # 18.04 (LTS2023)
    ("ubuntu", "focal", "deb", ubuntu_deps_2, install_deb),  # 20.04 (LTS2025)
    #("ubuntu", "groovy", "deb", ubuntu_deps_2, install_deb),  # 20.10 2021-07 # not yet in packagecloud.io, as of 2021-01-25
    ("debian", "stretch", "deb", ubuntu_deps, install_deb),  # 9
    ("debian", "buster", "deb", ubuntu_deps_2, install_deb),  # 10
    ("fedora", "26", "rpm", fedora_deps, install_rpm),  # EOL 2018-05-29
    ("fedora", "27", "rpm", fedora_deps, install_rpm),  # EOL 2018-11-30
    ("fedora", "28", "rpm", fedora_deps, install_rpm),  # EOL 2019-05-28
    ("fedora", "29", "rpm", fedora_deps, install_rpm),  # EOL 2019-11-30
    ("fedora", "30", "rpm", fedora_deps, install_rpm),  # EOL 2020-05-26
    ("fedora", "31", "rpm", fedora_deps, install_rpm),  # EOL 2020-11-24
    ("fedora", "32", "rpm", fedora_deps, install_rpm),
    #("fedora", "33", "rpm", fedora_deps, install_rpm), # not yet in packagecloud.io, as of 2021-01-25
]
