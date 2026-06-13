$version: "2"

namespace guardian.products.aisucks

@readonly
@http(method: "GET", uri: "/api/v1/hello", code: 200)
operation Hello {
    output: HelloOutput
    errors: [AisucksError]
}

structure HelloOutput {
    @required
    message: String

    @required
    service: String

    @required
    version: String
}

@error("server")
structure AisucksError {
    @required
    message: String
}
