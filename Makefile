GOARCH ?= $(shell go env GOARCH)
PACKAGE := kindle-sync-linux-$(GOARCH)
BUILD := dist/$(PACKAGE)

.PHONY: test plugin dist
test:
	go test ./...
	python3 -B -m unittest discover -s helper/tests -p 'test_*.py'

plugin: dist/kindle-notes.xpi

dist/kindle-notes.xpi: zotero-plugin/manifest.json zotero-plugin/bootstrap.js
	mkdir -p dist
	cd zotero-plugin && python3 -m zipfile -c ../dist/kindle-notes.xpi manifest.json bootstrap.js

dist: dist/kindle-notes.xpi
	mkdir -p $(BUILD)/bin $(BUILD)/helper $(BUILD)/deploy $(BUILD)/zotero-plugin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -o $(BUILD)/bin/kindle-sync ./cmd/kindle-sync
	cp helper/zotero_attachment.py helper/requirements.lock helper/README.md $(BUILD)/helper/
	cp deploy/config.example.json deploy/kindle-sync.service deploy/kindle-sync.timer deploy/install.sh $(BUILD)/deploy/
	cp dist/kindle-notes.xpi zotero-plugin/README.md $(BUILD)/zotero-plugin/
	cp README.md $(BUILD)/
	COPYFILE_DISABLE=1 tar --format=ustar -C dist -czf dist/$(PACKAGE).tar.gz $(PACKAGE)
