#!/bin/sh
set -e
# XXX /etc/openpvn is a volume so it gets wiped on container startup :(
export OPENVPN=/etc/openvpn.temp
export EASYRSA_PKI=$OPENVPN/pki
export EASYRSA_VARS_FILE=$OPENVPN/vars
ovpn_genconfig -u tcp://VPNSERVER
echo VPNSERVER | OVPN_CN=VPNSERVER ovpn_initpki nopass
