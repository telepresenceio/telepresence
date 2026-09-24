

update-dependencies: $(dir $(shell find . -name go.mod))
	for dir in $?; do\
 		(cd $$dir && \
 		 (go get -u ./... && \
 		   (grep -q gvisor.dev go.mod && go get -u gvisor.dev/gvisor@go) \
 		 ) || \
 		 go get -u .);\
 	done
	curl -sfL https://api.github.com/repos/docker/compose/releases/latest | jq -r .tag_name > build-aux/docker-compose.version
	$(MAKE) clobber generate check-unit

