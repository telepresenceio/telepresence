

update-dependencies: $(dir $(shell find . -name go.mod))
	for dir in $?; do\
 		(cd $$dir && \
 		 (go get -u ./... && \
 		   (grep -q gvisor.dev go.mod && go get -u gvisor.dev/gvisor@go) \
 		 ) || \
 		 go get -u .);\
 	done
	$(MAKE) clobber generate check-unit

