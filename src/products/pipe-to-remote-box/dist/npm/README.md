# npm distribution templates

These five directories are package templates, not publishable working copies.
They deliberately carry version `0.0.0-dev` and no checked-in binaries.

For a stable release, `stage-packages.sh` copies the four already
checksum-verified and Sigstore-verified GitHub Release binaries into their
matching platform packages, injects the stable version into every package and
optional-dependency pin, and copies the product's complete Apache-2.0 license
and third-party notices into all five packages. The four platform packages are
published before the meta package, so an install can never resolve a meta
version whose binary packages do not exist.

There are no npm lifecycle scripts. The meta package's small Node shim selects
one `optionalDependency` using `process.platform` and `process.arch`, then
executes that package's byte-identical release binary.
