#!/bin/sh -ex

# Prepare a development environment for running telepresence and its test
# suite.  These steps should typically only be required once to prepare the
# environment.

case "$(uname -s)" in
     Darwin)
	 brew update > /dev/null
	 brew cask install osxfuse
	 brew install python3 sshfs
	 ;;

     Linux)
	 sudo apt install sshfs conntrack
	 ;;
     *)
	 echo "Unknown platform."
	 exit 1
esac

# Newer Ruby needed for Package Cloud
rvm install 2.1

# Record some debugging info:
python --version
python2 --version
python3 --version
ruby --version

# Make sure gcloud is installed:
./ci/setup-gcloud.sh

# Make sure torsocks is installed:
./ci/build-torsocks.sh
