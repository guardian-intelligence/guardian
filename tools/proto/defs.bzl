load("@rules_buf//buf:defs.bzl", "buf_breaking_test", "buf_lint_test")

def guardian_buf_lint_test(name, targets, config = "//src/proto:buf.yaml", module = "", visibility = None):
    """Lint proto_library targets with the repo-pinned Buf toolchain."""
    buf_lint_test(
        name = name,
        config = config,
        module = module,
        targets = targets,
        visibility = visibility,
    )

def guardian_buf_breaking_test(
        name,
        targets,
        against,
        config = "//src/proto:buf.yaml",
        exclude_imports = True,
        module = "",
        visibility = None):
    """Check proto_library targets against a checked-in Buf image baseline."""
    buf_breaking_test(
        name = name,
        against = against,
        config = config,
        exclude_imports = exclude_imports,
        module = module,
        targets = targets,
        visibility = visibility,
    )

def guardian_go_proto_codegen_check(
        name,
        proto_srcs,
        proto_files,
        proto_path,
        generated_sources,
        generated_root,
        go_module,
        descriptor_srcs = "@protobuf//:descriptor_proto_srcs",
        protoc = "@protobuf//:protoc",
        visibility = None):
    """Verify checked-in Go proto/connect output is current.

    Mirrors guardian_ts_proto_codegen_check: the plugins are the go.mod-pinned
    protoc-gen-go and protoc-gen-connect-go built by rules_go, so regeneration
    drift (or hand-edits to gen/) fails here before merge. The `protoc`
    version header line differs between generators (buf writes "(unknown)",
    protoc writes its own version), so it is normalized out of the diff.
    """
    proto_file_args = " \\\n  ".join(proto_files)

    native.genrule(
        name = name,
        srcs = proto_srcs + [
            generated_sources,
            descriptor_srcs,
        ],
        outs = [name + ".stamp"],
        tools = [
            protoc,
            "@org_golang_google_protobuf//cmd/protoc-gen-go",
            "@com_connectrpc_connect//cmd/protoc-gen-connect-go",
        ],
        cmd = """
set -euo pipefail
execroot="$$(pwd)"
protoc="$$execroot/$(location {protoc})"
descriptor="$(location {descriptor_srcs})"
proto_include="$${{descriptor%/google/protobuf/descriptor.proto}}"
gen_go="$$execroot/$(location @org_golang_google_protobuf//cmd/protoc-gen-go)"
gen_connect="$$execroot/$(location @com_connectrpc_connect//cmd/protoc-gen-connect-go)"
out="$(@D)/go-generated"
rm -rf "$$out"
mkdir -p "$$out"
"$$protoc" \
  --proto_path={proto_path} \
  --proto_path="$$proto_include" \
  --plugin=protoc-gen-go="$$gen_go" \
  --plugin=protoc-gen-connect-go="$$gen_connect" \
  --go_out="$$out" --go_opt=module={go_module} \
  --connect-go_out="$$out" --connect-go_opt=module={go_module} \
  {proto_files}
normalize() {{
  find "$$1" -type f -name '*.go' -exec sed -i -e '/^\\/\\/ \\tprotoc  */d' {{}} +
}}
staged="$(@D)/go-checked-in"
rm -rf "$$staged"
mkdir -p "$$staged"
for f in $(locations {generated_sources}); do
  rel="$${{f#{generated_root}/}}"
  mkdir -p "$$staged/$$(dirname "$$rel")"
  cp "$$f" "$$staged/$$rel"
done
normalize "$$out"
normalize "$$staged"
diff -ru "$$staged" "$$out"
printf 'go proto generated code is current\\n' > "$@"
""".format(
            descriptor_srcs = descriptor_srcs,
            generated_root = generated_root,
            generated_sources = generated_sources,
            go_module = go_module,
            proto_files = proto_file_args,
            proto_path = proto_path,
            protoc = protoc,
        ),
        visibility = visibility,
    )

def guardian_ts_proto_codegen_check(
        name,
        proto_srcs,
        proto_files,
        proto_path,
        generated_sources,
        generated_root,
        opts = ["target=ts", "import_extension=js"],
        descriptor_srcs = "@protobuf//:descriptor_proto_srcs",
        node = "//src/products/viteplus-monorepo:vp_node",
        plugin = "//src/products/viteplus-monorepo:node_modules/@bufbuild/protoc-gen-es",
        protoc = "@protobuf//:protoc",
        visibility = None):
    """Verify checked-in TypeScript proto output is current.

    Product packages declare what gets generated. This repository macro owns the
    pinned local protoc/protoc-gen-es invocation so raw compiler plumbing does
    not spread across BUILD files.
    """
    opt_arg = ",".join(opts)
    proto_file_args = " \\\n  ".join(proto_files)

    native.genrule(
        name = name,
        srcs = proto_srcs + [
            generated_sources,
            descriptor_srcs,
        ],
        outs = [name + ".stamp"],
        tools = [
            node,
            plugin,
            protoc,
        ],
        cmd = """
set -euo pipefail
execroot="$$(pwd)"
node="$$execroot/$(location {node})"
protoc="$$execroot/$(location {protoc})"
descriptor="$(location {descriptor_srcs})"
proto_include="$${{descriptor%/google/protobuf/descriptor.proto}}"
plugin=""
for candidate in $(locations {plugin}); do
  case "$$candidate" in
    */bin/protoc-gen-es)
      plugin="$$execroot/$$candidate"
      ;;
    */node_modules/@bufbuild/protoc-gen-es)
      plugin="$$execroot/$$candidate/bin/protoc-gen-es"
      ;;
  esac
done
test -n "$$plugin"
export PATH="$$(dirname "$$node"):$${{PATH:-/usr/bin:/bin}}"
out="$(@D)/ts-sdk-generated"
rm -rf "$$out"
mkdir -p "$$out"
"$$protoc" \
  --proto_path={proto_path} \
  --proto_path="$$proto_include" \
  --plugin=protoc-gen-es="$$plugin" \
  --es_out="$$out" \
  --es_opt={opt_arg} \
  {proto_files}
find "$$out" -type f -name '*.ts' -exec sh -c '
for file do
  tmp="$$file.tmp"
  awk '"'"'
    {{ lines[NR] = $$0 }}
    END {{
      n = NR
      while (n > 0 && lines[n] == "") {{
        n--
      }}
      for (i = 1; i <= n; i++) {{
        print lines[i]
      }}
    }}
  '"'"' "$$file" > "$$tmp"
  mv "$$tmp" "$$file"
done
' sh {{}} +
diff -ru {generated_root} "$$out"
printf 'typescript proto generated code is current\\n' > "$@"
""".format(
            descriptor_srcs = descriptor_srcs,
            generated_root = generated_root,
            node = node,
            opt_arg = opt_arg,
            plugin = plugin,
            proto_files = proto_file_args,
            proto_path = proto_path,
            protoc = protoc,
        ),
        visibility = visibility,
    )
