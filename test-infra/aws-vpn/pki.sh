#!/usr/bin/env bash
# Script adapted from https://community.axway.com/s/question/0D52X000065Ykx2SAC/example-scripts-to-create-certificate-chain-with-openssl

mkdir certs

subj='/C=CA'

set -e

#Generate CA Certificate
#Generate private Key
openssl genrsa -out certs/CA.key 2048
#Generate CA CSR
openssl req -new -sha256 -key certs/CA.key -out certs/CA.csr -subj "$subj/CN=CA CERTIFICATE"
#Generate CA Certificate (10 years)
openssl x509 -signkey certs/CA.key -in certs/CA.csr -req -days 3650 -out certs/CA.pem

#--------------------------------------------------------------------------------------
#Generate Intermediary CA Certificate
#Generate private Key

openssl genrsa -out certs/CA_Intermediary.key 2048

#Create Intermediary CA CSR
openssl req -new -sha256 -key certs/CA_Intermediary.key -out certs/CA_Intermediary.csr -subj "$subj/CN=CA INTERMEDIARY CERTIFICATE"

#Generate Server Certificate (10 years)
openssl x509 -req -in certs/CA_Intermediary.csr -CA certs/CA.pem -CAkey certs/CA.key -CAcreateserial -out certs/CA_Intermediary.crt -days 3650 -sha256

cat certs/CA.pem certs/CA_Intermediary.crt > certs/ca-chain.crt
 
#--------------------------------------------------------------------------------------
#Generate VPN Certificate signed by Intermediary CA
#Generate private Key
openssl genrsa -out certs/VPNCert.key 2048

#Create Client CSR
openssl req -new -sha256 -key certs/VPNCert.key -out certs/VPNCert.csr -subj "$subj/CN=client"

#Generate Client Certificate
openssl x509 -req -in certs/VPNCert.csr -CA certs/CA.pem -CAkey certs/CA.key -CAcreateserial -out certs/VPNCert.crt -days 3650 -sha256

#View Certificate
openssl x509 -text -noout -in certs/VPNCert.crt
