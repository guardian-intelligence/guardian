set -u
dir=$1
printf 'directory=%s\n' "$dir"
if [ ! -e "$dir" ]; then echo 'status=missing'; exit 3; fi
if [ ! -d "$dir" ]; then echo 'status=not-a-directory'; exit 4; fi
echo 'status=present'
printf 'modified='; stat -c '%y' "$dir" 2>/dev/null || stat -f '%Sm' "$dir" 2>/dev/null || echo unavailable
printf 'entries='; find "$dir" -mindepth 1 -maxdepth 1 \
  -exec sh -c 'for path do printf x; done' sh {} + 2>/dev/null \
  | wc -c | tr -d ' '
echo 'recent_paths_hex='
find "$dir" -mindepth 1 -maxdepth 2 -type f -mtime -7 \
  -exec sh -c 'for path do printf %s "$path" | od -An -v -tx1 | tr -d " \n"; printf "\n"; done' sh {} + \
  2>/dev/null | head -n 20
if [ -d "$dir/.git" ] && [ ! -L "$dir/.git" ] && command -v git >/dev/null 2>&1; then
  revision=$(GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_ATTR_NOSYSTEM=1 \
    GIT_OPTIONAL_LOCKS=0 GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 GIT_PAGER=cat \
    git --no-optional-locks -c color.ui=false -c log.showSignature=false \
      -C "$dir" rev-parse --verify HEAD 2>/dev/null \
      | grep -E '^[0-9a-f]{40,64}$' | head -n 1) || revision=
  printf 'git_revision=%s\n' "$revision"
  commit_time=$(GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_ATTR_NOSYSTEM=1 \
    GIT_OPTIONAL_LOCKS=0 GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 GIT_PAGER=cat \
    git --no-optional-locks -c color.ui=false -c log.showSignature=false \
      -C "$dir" log -1 --no-show-signature --format='%ct' 2>/dev/null \
      | grep -E '^[0-9]+$' | head -n 1) || commit_time=
  printf 'git_commit_time=%s\n' "$commit_time"
fi
