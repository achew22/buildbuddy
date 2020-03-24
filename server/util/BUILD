load("@io_bazel_rules_go//go:def.bzl", "go_binary", "go_library")

package(default_visibility = ["//visibility:public"])

go_library(
    name = "healthcheck",
    srcs = ["healthcheck.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/util/healthcheck",
)

go_library(
    name = "protofile",
    srcs = ["protofile.go"],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/util/protofile",
    deps = [
        "//server:interfaces",
        "@com_github_golang_protobuf//proto:go_default_library",
    ],
)

go_library(
    name = "status",
    srcs = [
        "status.go",
    ],
    importpath = "github.com/buildbuddy-io/buildbuddy/server/util/status",
    deps = [
        "@org_golang_google_grpc//codes:go_default_library",
        "@org_golang_google_grpc//status:go_default_library",
    ],
)