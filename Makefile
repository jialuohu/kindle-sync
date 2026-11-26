GOARCH ?= $(shell go env GOARCH)
PACKAGE := kindle-sync-linux-$(GOARCH)
BUILD := dist/$(PACKAGE)

.PHONY: test dist
test:
	go test ./...
	python3 -B -m unittest discover -s helper/tests -p 'test_*.py'

dist:
	mkdir -p $(BUILD)/bin $(BUILD)/helper $(BUILD)/deploy
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -o $(BUILD)/bin/kindle-sync ./cmd/kindle-sync
	cp helper/zotero_attachment.py helper/requirements.lock $(BUILD)/helper/
	cp deploy/config.example.json deploy/kindle-sync.service deploy/kindle-sync.timer deploy/install.sh $(BUILD)/deploy/
	cp README.md $(BUILD)/
	COPYFILE_DISABLE=1 tar --format=ustar -C dist -czf dist/$(PACKAGE).tar.gz $(PACKAGE)
