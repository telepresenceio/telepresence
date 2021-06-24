#!/usr/bin/env bash

for _ in $(seq 1 10); do
    make tools/bin/ko
    go test -timeout=15m -count 1 ./... || exit 1
done
