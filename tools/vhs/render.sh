#!/bin/sh
# Regenerate the README screenshots in assets/ with VHS.
#
# Requires: vhs (https://github.com/charmbracelet/vhs), python3, go.
# Everything runs against synthetic demo data; no real session stores are read.
#
# Usage:  tools/vhs/render.sh            # render all tapes
#         tools/vhs/render.sh dashboard  # render one tape by name
set -eu

cd "$(dirname "$0")/../.."          # repo root; vhs tapes use repo-relative paths
ROOT=$(pwd)
DEMO="$ROOT/tools/vhs/.demo"        # gitignored scratch: fixtures, launcher, index db

go build -o "$ROOT/tokenhawk" .

mkdir -p "$DEMO"
: > "$DEMO/empty.toml"

# Launcher the tapes invoke; points Tokenhawk at the fixture tree and a throwaway index.
cat > "$DEMO/run.sh" <<EOF
#!/bin/sh
cd "$ROOT"
# Screenshots are color assets even when the invoking shell prefers NO_COLOR.
unset NO_COLOR
exec ./tokenhawk --config "$DEMO/empty.toml" \\
  --claude-dir "$DEMO/fixtures/claude" --codex-dir "$DEMO/fixtures/codex" \\
  --gemini-dir "$DEMO/fixtures/gemini" --pi-dir "$DEMO/fixtures/pi" \\
  --opencode-db "$DEMO/none.db" --db "$DEMO/index.db" --active-window 5m
EOF
chmod +x "$DEMO/run.sh"

tapes=${*:-"dashboard detail compact"}
for t in $tapes; do
  # Fresh fixtures + index per tape so the active/inactive split (a wall-clock
  # threshold) is identical every run and can't drift between renders.
  python3 tools/vhs/gen_fixtures.py "$DEMO/fixtures" >/dev/null
  rm -f "$DEMO/index.db" "$DEMO/index.db-wal" "$DEMO/index.db-shm"
  echo "rendering $t..."
  vhs "tools/vhs/$t.tape"
done

rm -f tools/vhs/.render.gif
echo "done -> assets/"
