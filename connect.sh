#!/bin/sh
# Run remote pod:
kubectl apply -f remotepod.yaml
sleep 10 # Wait for pod to deploy

# Copy certificate locally, make sure it points at hostname IP as seen from
# Docker's perspective:
CONFIG_DIR=$(mktemp -d)
VPN_CONF=$CONFIG_DIR/vpn.conf
kubectl cp telepresence:/client-config/vpn.conf $VPN_CONF
DOCKER_IP=$(ifconfig docker0 | grep -Po 'inet addr:\K[^ ]*')
sed -i -- "s/VPNSERVER/$DOCKER_IP/g" $VPN_CONF
kubectl port-forward telepresence 1194:1194 &
sleep 5


sudo docker run --cap-add=NET_ADMIN --device /dev/net/tun --name k8s-vpn -v /tmp/cert:/cert --rm farmcoolcow/openvpn-client --config $VPN_CONF
