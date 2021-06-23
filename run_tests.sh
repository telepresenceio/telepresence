#!/usr/bin/env bash

for _ in $(seq 1 10); do
    make tools/bin/ko
    go test -count 1 ./... || exit 1
done
