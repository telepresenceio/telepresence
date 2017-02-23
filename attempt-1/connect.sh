#!/bin/sh
set -e
set -x
# Run remote pod:
kubectl apply -f remotepod.yaml
sleep 10 # Wait for pod to deploy

# Copy certificate locally, make sure it points at hostname IP as seen from
# Docker's perspective:
CONFIG_DIR=$(mktemp -d)
VPN_CONF=$CONFIG_DIR/vpn.conf
kubectl cp telepresence:/client-config/vpn.conf $VPN_CONF
sed -i -- "s/VPNSERVER/localhost/g" $VPN_CONF
sudo docker run --rm --name=k8s-vpn --user $(id -u itamarst) -v $HOME/.kube:/config/.kube:ro -v $HOME/.minikube:$HOME/.minikube:ro  wernight/kubectl port-forward telepresence 1194:1194 &
sleep 5

# Need something like this with resolv-conf updater for DNS; revisit:
#sudo docker run --cap-add=NET_ADMIN --device /dev/net/tun --rm --net container:k8s-vpn -v $CONFIG_DIR:/vpn -e DNS=true dperson/openvpn-client

sudo docker run --cap-add=NET_ADMIN --device /dev/net/tun --rm --net container:k8s-vpn -v $CONFIG_DIR:/config -e DNS=true farmcoolcow/openvpn-client --config /config/vpn.conf
