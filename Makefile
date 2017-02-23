default:
	echo "Run 'make build' to build Docker image."

build:
	cp connect.py local
	cd local && docker build . -t datawire/local-telepresence
	rm local/connect.py
