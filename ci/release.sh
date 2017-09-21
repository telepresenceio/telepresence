#!/bin/bash
# Run the release!
set -e

if [[ "$TRAVIS_OS_NAME" == "osx" ]]; then
    exit 0;
fi

# Login to Docker Hub
docker login -p "$DOCKER_PASSWORD" -u d6eautomaton

# Store the SSH key used to push to github.com/datawire/homebrew-blackbird; this
# key is set as environment variable on Travis repo:
echo -e "$HOMEBREW_KEY" > packaging/homebrew.rsa
chmod 600 packaging/homebrew.rsa

# Add ssh keys we need to push to github.com/datawire/homebrew-blackbird:
eval $(ssh-agent)
ssh-add packaging/homebrew.rsa

# Install package cloud CLI:
sudo gem install package_cloud

# Run the release:
make release
