.PHONY: build sandbox test clean

# Build the forge binary
build:
	go build -o forge ./cmd/forge/

# Build the sandbox Docker image
# Copies dotfiles into build context (Docker can't follow symlinks outside context)
sandbox:
	rm -rf .build-ctx
	mkdir -p .build-ctx
	cp Dockerfile.sandbox .build-ctx/Dockerfile
	cp sandbox-zshrc .build-ctx/sandbox-zshrc
	cp sandbox-tmux.conf .build-ctx/sandbox-tmux.conf
	cp -rL ~/.dotfiles .build-ctx/dotfiles
	rm -rf .build-ctx/dotfiles/.git
	rm -f .build-ctx/dotfiles/wm/.yabairc .build-ctx/dotfiles/wm/.skhdrc .build-ctx/dotfiles/wm/cycle-space.sh
	cp ~/.claude.json .build-ctx/claude.json
	mkdir -p .build-ctx/claude-settings
	cp ~/.claude/settings.json .build-ctx/claude-settings/settings.json
	security find-generic-password -s "Claude Code-credentials" -w > .build-ctx/claude-settings/.credentials.json 2>/dev/null || true
	docker build -t forge-sandbox .build-ctx
	rm -rf .build-ctx

test:
	go test ./... -count=1 -timeout 60s

clean:
	rm -f forge
	rm -rf .build-ctx
