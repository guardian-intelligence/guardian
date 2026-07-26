## Vite+

Websites in apps/
Reusable modules in packages/

viteplus commands (from https://github.com/voidzero-dev/vite-plus)

```
add - Add packages to dependencies
remove (rm, un, uninstall) - Remove packages from dependencies
update (up) - Update packages to latest versions
dedupe - Deduplicate dependencies
outdated - Check outdated packages
list (ls) - List installed packages
why (explain) - Show why a package is installed
info (view, show) - View package metadata from the registry
link (ln) / unlink - Manage local package links
rebuild - Rebuild native modules
pm - Forward a command to the package manager
```

<common_mistakes>

- Don't use raw `pnpm` -- use `vp pm` to forward to the package manager if necessary (usually not)
- Don't use `corepack` -- unnecessary.
  </common_mistakes>
