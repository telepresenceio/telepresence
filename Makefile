default:
	echo "Run 'make build' to build Docker image."

build:
	cd local && docker build . -t datawire/local-telepresence
	cd remote && docker build . -t datawire/remote-telepresence:dev
