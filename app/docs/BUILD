load("@npm_bazel_typescript//:index.bzl", "ts_library")

package(default_visibility = ["//visibility:public"])

ts_library(
    name = "docs",
    srcs = glob(["*.tsx"]),
    deps = [
        "//app/capabilities",
        "//app/service",
        "//proto:bazel_config_ts_proto",
        "@npm//@types/react",
        "@npm//protobufjs",
        "@npm//react",
    ],
)
