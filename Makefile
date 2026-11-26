GOARCH ?= $(shell go env GOARCH)
PACKAGE := kindle-sync-linux-$(GOARCH)
BUILD := dist/$(PACKAGE)
RELEASE_SOURCES := README.md docs/setup.md \
	helper/zotero_attachment.py helper/requirements.lock helper/README.md \
	deploy/config.example.json deploy/kindle-sync.service deploy/kindle-sync.timer deploy/install.sh \
	zotero-plugin/README.md
PLUGIN_FILES := manifest.json bootstrap.js

.PHONY: test plugin dist
test:
	go test ./...
	python3 -B -m unittest discover -s helper/tests -p 'test_*.py'

plugin: dist/kindle-notes.xpi

dist/kindle-notes.xpi: $(addprefix zotero-plugin/,$(PLUGIN_FILES)) scripts/package.py
	mkdir -p dist
	python3 scripts/package.py zip zotero-plugin $@ $(PLUGIN_FILES)

dist: dist/kindle-notes.xpi
	mkdir -p $(BUILD)/bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -buildvcs=false -o $(BUILD)/bin/kindle-sync ./cmd/kindle-sync
	set -e; for file in $(RELEASE_SOURCES); do \
		mkdir -p "$(BUILD)/$$(dirname "$$file")"; cp "$$file" "$(BUILD)/$$file"; \
	done
	cp dist/kindle-notes.xpi $(BUILD)/zotero-plugin/
	python3 scripts/package.py tar $(BUILD) $(BUILD).tar.gz $(RELEASE_SOURCES) bin/kindle-sync zotero-plugin/kindle-notes.xpi
