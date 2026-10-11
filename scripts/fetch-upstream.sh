#!/bin/sh
# Fetches the upstream Node implementation this port follows into third_party/upstream (git-ignored).
set -eu
REV=67d13b0
DIR="$(cd "$(dirname "$0")/.." && pwd)/third_party/upstream"
if [ -d "$DIR/.git" ] && [ "$(git -C "$DIR" rev-parse --short=7 HEAD)" = "$REV" ]; then
	echo "upstream already at $REV"
	exit 0
fi
rm -rf "$DIR"
git clone --quiet https://github.com/marclou/mailcheap "$DIR"
git -C "$DIR" checkout --quiet "$REV"
echo "upstream at $(git -C "$DIR" rev-parse --short=7 HEAD)"
